package users

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"drivethrough/internal/auth"
	"drivethrough/internal/models"
	"drivethrough/internal/sms"
	"drivethrough/pkg/apperr"
	"drivethrough/pkg/validator"
)

type Service struct {
	repo        *Repository
	tokenIssuer auth.TokenIssuer
	otp         *auth.OTPManager
	smsSender   sms.Sender
}

func NewService(repo *Repository, tokenIssuer auth.TokenIssuer, otp *auth.OTPManager, smsSender sms.Sender) *Service {
	return &Service{repo: repo, tokenIssuer: tokenIssuer, otp: otp, smsSender: smsSender}
}

func (s *Service) RequestOTP(ctx context.Context, phone string) error {
	if !validator.IsValidPhone(phone) {
		return apperr.BadRequest("invalid phone number", nil)
	}

	code, err := s.otp.Generate(ctx, phone)
	if err != nil {
		return apperr.Wrap(500, "OTP_FAILED", "failed to generate otp", err, nil)
	}

	if s.smsSender != nil {
		msg := fmt.Sprintf("Your DriveThrough verification code is %s. Valid for 5 minutes.", code)
		if err := s.smsSender.Send(ctx, phone, msg); err != nil {
			return apperr.Wrap(500, "SMS_FAILED", "failed to send otp", err, nil)
		}
	}

	return nil
}

// LoginWithOTP verifies a phone OTP and signs the account in, registering it on
// first use.
//
// role is only ever a request for CUSTOMER or DRIVER. ADMIN is rejected outright:
// the role arrived from an unauthenticated request body, so honouring it would
// let anyone POST {"phone": ..., "code": ..., "role": "ADMIN"} and receive an
// admin token. Admin accounts are provisioned out of band.
func (s *Service) LoginWithOTP(ctx context.Context, phone, code, name string, role models.UserRole) (*models.User, *models.TokenPair, error) {
	if !validator.IsValidPhone(phone) {
		return nil, nil, apperr.BadRequest("invalid phone number", nil)
	}

	role, err := s.resolveSignupRole(role)
	if err != nil {
		return nil, nil, err
	}

	if err := s.otp.Verify(ctx, phone, code); err != nil {
		return nil, nil, apperr.BadRequest("invalid or expired otp", nil)
	}

	user, err := s.repo.GetByPhone(ctx, phone)
	if err != nil {
		if apperr.IsNotFound(err) {
			if name == "" {
				return nil, nil, apperr.BadRequest("name required for new user", nil)
			}
			user, err = s.repo.CreateUser(ctx, name, phone, role)
			if err != nil {
				return nil, nil, apperr.Wrap(500, "SIGNUP_FAILED", "failed to create user", err, nil)
			}
		} else {
			return nil, nil, err
		}
	}

	if !user.IsActive {
		return nil, nil, apperr.Forbidden("account is deactivated")
	}

	tokens, err := s.tokenIssuer.Issue(user)
	if err != nil {
		return nil, nil, apperr.Wrap(500, "TOKEN_FAILED", "failed to issue tokens", err, nil)
	}

	return user, tokens, nil
}

// resolveSignupRole validates a client-supplied signup role.
func (s *Service) resolveSignupRole(role models.UserRole) (models.UserRole, error) {
	switch role {
	case "", models.RoleCustomer:
		return models.RoleCustomer, nil
	case models.RoleDriver:
		return models.RoleDriver, nil
	case models.RoleAdmin:
		// Deliberately a 403 rather than a silent downgrade: a caller asking for
		// admin is either a bug or an attack, and both need to be visible.
		return "", apperr.Forbidden("admin accounts cannot be self-registered")
	default:
		return "", apperr.BadRequest("invalid role", nil)
	}
}

func (s *Service) RefreshToken(ctx context.Context, refreshToken string) (*models.TokenPair, error) {
	claims, err := s.tokenIssuer.Parse(refreshToken)
	if err != nil {
		return nil, apperr.Unauthorized("invalid refresh token")
	}

	if claims.Role != "" {
		return nil, apperr.Unauthorized("invalid refresh token")
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, apperr.BadRequest("invalid token subject", nil)
	}

	user, err := s.repo.GetByID(ctx, userID)
	if err != nil {
		return nil, apperr.Unauthorized("user not found for token")
	}

	if !user.IsActive {
		return nil, apperr.Forbidden("account is deactivated")
	}

	tokens, err := s.tokenIssuer.Issue(user)
	if err != nil {
		return nil, apperr.Wrap(500, "TOKEN_FAILED", "failed to issue tokens", err, nil)
	}

	return tokens, nil
}

func (s *Service) UpdateProfile(ctx context.Context, userID uuid.UUID, name string) (*models.User, error) {
	if name == "" {
		return nil, apperr.BadRequest("name cannot be empty", nil)
	}
	if err := s.repo.UpdateName(ctx, userID, name); err != nil {
		return nil, apperr.Wrap(500, "UPDATE_FAILED", "failed to update profile", err, nil)
	}
	return s.repo.GetByID(ctx, userID)
}

func (s *Service) GetProfile(ctx context.Context, userID uuid.UUID) (*models.User, error) {
	return s.repo.GetByID(ctx, userID)
}

func (s *Service) Logout(ctx context.Context, userID uuid.UUID, accessToken string) error {
	if accessToken == "" {
		return nil
	}
	return s.tokenIssuer.Logout(ctx, userID, accessToken)
}
