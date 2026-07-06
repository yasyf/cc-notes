// The sticky time axis: tick labels along the top, drawn against the current
// display scale. Recessive chrome — hairline ticks, muted text.

import { AXIS_HEIGHT } from "./geometry";
import type { Tick } from "./scale";

export function Axis({ ticks, width }: { ticks: Tick[]; width: number }) {
  return (
    <svg
      className="tl-axis"
      width={width}
      height={AXIS_HEIGHT}
      role="presentation"
    >
      <line
        x1={0}
        x2={width}
        y1={AXIS_HEIGHT - 0.5}
        y2={AXIS_HEIGHT - 0.5}
        className="tl-axis-baseline"
      />
      {ticks.map((t) => (
        <g key={t.value.getTime()} transform={`translate(${t.x},0)`}>
          <line y1={AXIS_HEIGHT - 6} y2={AXIS_HEIGHT} className="tl-axis-tick" />
          <text x={3} y={AXIS_HEIGHT - 9} className="tl-axis-label">
            {t.label}
          </text>
        </g>
      ))}
    </svg>
  );
}
