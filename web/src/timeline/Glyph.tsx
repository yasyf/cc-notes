// Renders one event glyph centred at the origin from a MarkSpec. A surface-
// coloured ring (paint-order stroke) keeps the mark legible where markers
// overlap, per the dataviz mark spec.

import { glyphGeom, STROKE_SHAPES, type MarkSpec } from "./marks";

export function Glyph({ spec, size }: { spec: MarkSpec; size: number }) {
  const geom = glyphGeom(spec.shape, size);
  const stroked = spec.hollow || STROKE_SHAPES.has(spec.shape);
  const fill = stroked ? "none" : spec.color;
  const stroke = stroked ? spec.color : "var(--viz-surface)";
  const strokeWidth = stroked ? 2 : 1.5;
  const common = {
    fill,
    stroke,
    strokeWidth,
    strokeLinejoin: "round" as const,
    strokeLinecap: "round" as const,
    paintOrder: "stroke" as const,
  };
  switch (geom.kind) {
    case "circle":
      return <circle r={geom.r} {...common} />;
    case "rect":
      return (
        <rect x={geom.x} y={geom.y} width={geom.w} height={geom.h} rx={geom.rx} {...common} />
      );
    case "polygon":
      return <polygon points={geom.points} {...common} />;
    case "path":
      return <path d={geom.d} {...common} />;
  }
}
