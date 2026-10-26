import type { Location } from '../../api/types'
import type { ConnectionState } from '../../api/tripSocket'

/**
 * Live tracking view.
 *
 * There is no map provider wired up: MAPBOX_ACCESS_TOKEN and GOOGLE_MAPS_API_KEY
 * are both parsed from the environment but unused, so a tile-based map would
 * render blank. Rather than show an empty grey box, this draws the route the
 * driver has actually travelled on a plain canvas — the path is real data from
 * the trip's location stream, and it works with no keys and no network.
 *
 * A tile layer can replace the body of this component once a provider key exists;
 * nothing above it needs to change.
 */
export function TrackMap({
  pickup,
  path,
  connection,
}: {
  pickup: Location
  path: Location[]
  connection: ConnectionState
}) {
  const points = path.length > 0 ? path : [pickup]

  // Fit to the travelled path with padding, falling back to a tight box around a
  // single point so a fresh trip is not divided by zero.
  const lats = points.map((p) => p.latitude)
  const lngs = points.map((p) => p.longitude)
  const minLat = Math.min(...lats)
  const maxLat = Math.max(...lats)
  const minLng = Math.min(...lngs)
  const maxLng = Math.max(...lngs)
  const spanLat = Math.max(maxLat - minLat, 0.002)
  const spanLng = Math.max(maxLng - minLng, 0.002)

  const project = (p: Location) => ({
    x: ((p.longitude - minLng) / spanLng) * 100,
    y: 100 - ((p.latitude - minLat) / spanLat) * 100,
  })

  const pts = points.map(project)
  const d = pts
    .map((p, i) => `${i === 0 ? 'M' : 'L'}${(p.x / 100) * 100},${(p.y / 100) * 100}`)
    .join(' ')
  const start = project(pickup)
  const head = pts[pts.length - 1]

  return (
    <div className="track">
      <svg
        viewBox="0 0 100 100"
        preserveAspectRatio="none"
        className="path"
        role="img"
        aria-label="Route travelled by the driver"
      >
        {/* preserveAspectRatio="none" stretches the viewBox to the box, so the
            stroke would be scaled non-uniformly; non-scaling-stroke keeps the
            line the same weight on both axes. */}
        <path
          d={d}
          fill="none"
          stroke="var(--primary)"
          strokeWidth={2.5}
          strokeLinecap="round"
          strokeLinejoin="round"
          vectorEffect="non-scaling-stroke"
        />
      </svg>

      <span className="marker pickup" style={{ left: `${start.x}%`, top: `${start.y}%` }} />
      {path.length > 0 && (
        <span className="marker" style={{ left: `${head.x}%`, top: `${head.y}%` }} />
      )}

      <div className={`conn ${connection}`}>
        <span className="led" />
        {connection === 'open'
          ? `live · ${path.length} update${path.length === 1 ? '' : 's'}`
          : connection}
      </div>
    </div>
  )
}