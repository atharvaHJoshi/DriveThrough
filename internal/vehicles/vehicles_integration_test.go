package vehicles_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"drivethrough/internal/models"
	"drivethrough/internal/testsupport"
	"drivethrough/internal/vehicles"
	"drivethrough/pkg/apperr"
)

type fixture struct {
	db       *testsupport.DB
	ownerID  testsupport.UUID
	otherID  testsupport.UUID
	vehicle  testsupport.UUID
	otherCar testsupport.UUID
}

func setup(t *testing.T) *fixture {
	t.Helper()
	db := testsupport.NewDB(t)
	db.Truncate(t)

	f := &fixture{db: db}
	f.ownerID = db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Owner', $1, 'CUSTOMER') RETURNING id`,
		uniquePhone("own"))
	f.otherID = db.UUID(t,
		`INSERT INTO users (name, phone, role) VALUES ('Other', $1, 'CUSTOMER') RETURNING id`,
		uniquePhone("oth"))

	f.vehicle = db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class, is_default)
		 VALUES ($1, $2, 'Toyota', 'Innova', 2020, 'MANUAL', 'PETROL', 'SUV', TRUE) RETURNING id`,
		f.ownerID, uniqueReg("own"))
	f.otherCar = db.UUID(t,
		`INSERT INTO vehicles (user_id, registration_number, make, model, year, transmission, fuel_type, class)
		 VALUES ($1, $2, 'Honda', 'City', 2021, 'AUTOMATIC', 'PETROL', 'SEDAN') RETURNING id`,
		f.otherID, uniqueReg("oth"))

	return f
}

func svc(db *testsupport.DB) *vehicles.Service {
	return vehicles.NewService(vehicles.NewRepository(db.Pool))
}

func str(s string) *string { return &s }

// TestPartialPatchLeavesOtherFieldsIntact is the regression test for the PATCH
// handler.
//
// The route is PATCH but the handler decoded into models.Vehicle and assigned
// every field, so a body carrying only {"year": 2021} silently blanked the make,
// model, transmission, fuel type, class and registration number — a customer's
// verified vehicle record was destroyed by an innocuous one-field edit.
func TestPartialPatchLeavesOtherFieldsIntact(t *testing.T) {
	f := setup(t)
	s := svc(f.db)

	year := 2024
	updated, err := s.UpdateVehicle(dbCtx(t), f.vehicle, f.ownerID, vehicles.UpdateVehicleInput{
		Year: &year,
	})
	if err != nil {
		t.Fatalf("patch year: %v", err)
	}

	if updated.Year != 2024 {
		t.Errorf("year = %d, want 2024", updated.Year)
	}
	// Everything not mentioned must be exactly as it was.
	if updated.Make != "Toyota" {
		t.Errorf("make = %q, want it preserved as Toyota", updated.Make)
	}
	if updated.Model != "Innova" {
		t.Errorf("model = %q, want it preserved as Innova", updated.Model)
	}
	if updated.Transmission != models.TransmissionManual {
		t.Errorf("transmission = %q, want it preserved as MANUAL", updated.Transmission)
	}
	if updated.FuelType != models.FuelPetrol {
		t.Errorf("fuel type = %q, want it preserved as PETROL", updated.FuelType)
	}
	if updated.Class != models.VehicleClassSUV {
		t.Errorf("class = %q, want it preserved as SUV", updated.Class)
	}
	if updated.RegistrationNumber == "" {
		t.Error("the registration number was blanked by a year-only patch")
	}
}

// TestPatchRejectsInvalidValues makes sure a rejected field leaves the row
// untouched rather than applying the good fields first.
func TestPatchRejectsInvalidValues(t *testing.T) {
	f := setup(t)
	s := svc(f.db)

	cases := map[string]vehicles.UpdateVehicleInput{
		"year out of range":    {Year: intPtr(1800)},
		"unknown transmission": {Transmission: transmissionPtr("FLYING")},
		"unknown fuel type":    {FuelType: fuelPtr("PLUTONIUM")},
		"unknown class":        {Class: classPtr("SPACESHIP")},
		"invalid registration": {RegistrationNumber: str("not a plate!")},
		"empty make":           {Make: str("")},
		"empty model":          {Model: str("")},
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			before, err := s.GetVehicleForOwner(dbCtx(t), f.vehicle, f.ownerID)
			if err != nil {
				t.Fatalf("read before: %v", err)
			}

			if _, err := s.UpdateVehicle(dbCtx(t), f.vehicle, f.ownerID, in); err == nil {
				t.Fatal("an invalid value was accepted")
			} else if !apperr.IsBadRequest(err) {
				t.Errorf("expected a bad request, got %v", err)
			}

			after, err := s.GetVehicleForOwner(dbCtx(t), f.vehicle, f.ownerID)
			if err != nil {
				t.Fatalf("read after: %v", err)
			}
			if after.Make != before.Make || after.Model != before.Model || after.Year != before.Year {
				t.Error("a rejected patch still modified the vehicle")
			}
		})
	}
}

// TestPatchRejectsEmptyBody stops a no-op request from looking like a success.
func TestPatchRejectsEmptyBody(t *testing.T) {
	f := setup(t)
	s := svc(f.db)

	if _, err := s.UpdateVehicle(dbCtx(t), f.vehicle, f.ownerID, vehicles.UpdateVehicleInput{}); err == nil {
		t.Fatal("an empty patch was accepted")
	} else if !apperr.IsBadRequest(err) {
		t.Errorf("expected a bad request, got %v", err)
	}
}

// TestPatchIsScopedToTheOwner is the IDOR guard: the ownership predicate lives
// in the UPDATE, and a non-owner must not be able to tell the vehicle apart
// from one that does not exist.
func TestPatchIsScopedToTheOwner(t *testing.T) {
	f := setup(t)
	s := svc(f.db)

	make := "Renault"
	_, err := s.UpdateVehicle(dbCtx(t), f.vehicle, f.otherID, vehicles.UpdateVehicleInput{Make: &make})
	if err == nil {
		t.Fatal("a non-owner patched someone else's vehicle")
	}
	// Not-found rather than forbidden: reporting "forbidden" would confirm the
	// vehicle exists.
	if !apperr.IsNotFound(err) {
		t.Errorf("expected not found for a non-owner, got %v", err)
	}

	v, err := s.GetVehicleForOwner(dbCtx(t), f.vehicle, f.ownerID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if v.Make != "Toyota" {
		t.Errorf("make = %q, want the owner's value unchanged", v.Make)
	}
}

// TestDefaultIsOnlyAssignedToTheFirstVehicle covers the default-selection rule:
// a customer already owning a vehicle should not silently lose their default
// by adding a second car, so only the first vehicle is auto-defaulted and the
// switch is explicit.
func TestDefaultIsOnlyAssignedToTheFirstVehicle(t *testing.T) {
	f := setup(t)
	s := svc(f.db)

	added, err := s.AddVehicle(dbCtx(t), f.ownerID, models.Vehicle{
		RegistrationNumber: uniqueReg("new"),
		Make:               "Kia",
		Model:              "Seltos",
		Year:               2023,
		Transmission:       models.TransmissionAutomatic,
		FuelType:           models.FuelDiesel,
		Class:              models.VehicleClassSUV,
	})
	if err != nil {
		t.Fatalf("add vehicle: %v", err)
	}
	if added.IsDefault {
		t.Error("a second vehicle was auto-defaulted, displacing the existing default")
	}

	orig, err := s.GetVehicleForOwner(dbCtx(t), f.vehicle, f.ownerID)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	if !orig.IsDefault {
		t.Error("the original vehicle lost its default status by adding a second car")
	}

	// The switch is explicit.
	if err := s.SetDefaultVehicle(dbCtx(t), added.ID, f.ownerID); err != nil {
		t.Fatalf("set default: %v", err)
	}

	nowDefault, err := s.GetVehicleForOwner(dbCtx(t), added.ID, f.ownerID)
	if err != nil {
		t.Fatalf("read new default: %v", err)
	}
	if !nowDefault.IsDefault {
		t.Error("the explicitly selected vehicle is not default")
	}

	orig, err = s.GetVehicleForOwner(dbCtx(t), f.vehicle, f.ownerID)
	if err != nil {
		t.Fatalf("read original again: %v", err)
	}
	if orig.IsDefault {
		t.Error("the previous vehicle is still default after the switch")
	}
}

// TestDuplicateRegistrationIsRejected verifies the transaction rolls back the
// default-clearing when the insert fails.
func TestDuplicateRegistrationIsRejected(t *testing.T) {
	f := setup(t)
	s := svc(f.db)

	dup, err := s.AddVehicle(dbCtx(t), f.ownerID, models.Vehicle{
		RegistrationNumber: "KA01AB1234",
		Make:               "Kia",
		Model:              "Seltos",
		Year:               2023,
		Transmission:       models.TransmissionAutomatic,
		FuelType:           models.FuelDiesel,
		Class:              models.VehicleClassSUV,
	})
	if err != nil {
		t.Fatalf("add first: %v", dup)
	}

	var reg string
	if err := f.db.Pool.QueryRow(dbCtx(t),
		`SELECT registration_number FROM vehicles WHERE id = $1`, dup.ID).Scan(&reg); err != nil {
		t.Fatalf("read reg: %v", err)
	}

	// A second attempt with the same plate must fail without disturbing the
	// first vehicle's default flag.
	if _, err := s.AddVehicle(dbCtx(t), f.ownerID, models.Vehicle{
		RegistrationNumber: reg,
		Make:               "Kia",
		Model:              "Seltos",
		Year:               2023,
		Transmission:       models.TransmissionAutomatic,
		FuelType:           models.FuelDiesel,
		Class:              models.VehicleClassSUV,
	}); err == nil {
		t.Fatal("a duplicate registration number was accepted")
	}

	orig, err := s.GetVehicleForOwner(dbCtx(t), f.vehicle, f.ownerID)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	if !orig.IsDefault {
		t.Error("a failed insert cleared the owner's default vehicle")
	}
}

func intPtr(i int) *int                                                  { return &i }
func transmissionPtr(v models.TransmissionType) *models.TransmissionType { return &v }
func fuelPtr(v models.FuelType) *models.FuelType                         { return &v }
func classPtr(v models.VehicleClass) *models.VehicleClass                { return &v }

func dbCtx(t *testing.T) context.Context { return t.Context() }

var seq int

func uniquePhone(p string) string { seq++; return "+9170" + itoa(seq) + uuid.New().String()[:4] }
func uniqueReg(p string) string   { seq++; return "KA05ZZ" + itoa(1000+seq) }
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}
