import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { SparklineChart } from "@/components/sparkline-chart";

function paths(container: HTMLElement) {
  const [area, line] = Array.from(container.querySelectorAll("path"));
  return { area, line };
}

describe("SparklineChart", () => {
  it("draws a gradient area and a non-scaling line without recharts", () => {
    const { container } = render(
      <SparklineChart data={[{ value: 1 }, { value: 4 }, { value: 2 }]} color="#3b82f6" index={0} />,
    );

    const svg = container.querySelector("svg")!;
    expect(svg).toHaveAttribute("height", "40");
    expect(svg).toHaveAttribute("preserveAspectRatio", "none");
    const { area, line } = paths(container);
    expect(area.getAttribute("fill")).toMatch(/^url\(#sparkline-fill-/);
    expect(line).toHaveAttribute("stroke", "#3b82f6");
    expect(line).toHaveAttribute("vector-effect", "non-scaling-stroke");
    expect(line.getAttribute("d")).toMatch(/^M 0 \d/);
  });

  it("keeps the curve inside the chart for spiky data", () => {
    const { container } = render(
      <SparklineChart data={[{ value: 0 }, { value: 10 }, { value: 0 }, { value: 0 }]} color="#000" index={1} height={40} />,
    );

    const numbers = paths(container).line.getAttribute("d")!.match(/-?\d+(\.\d+)?/g)!.map(Number);
    const ys = numbers.filter((_, position) => position % 2 === 1);
    expect(Math.min(...ys)).toBeGreaterThanOrEqual(2);
    expect(Math.max(...ys)).toBeLessThanOrEqual(38);
  });

  it("draws a flat baseline for all-zero data", () => {
    const { container } = render(
      <SparklineChart data={[{ value: 0 }, { value: 0 }]} color="#000" index={2} />,
    );

    expect(paths(container).line.getAttribute("d")).toBe("M 0 38 C 33.333 38, 66.667 38, 100 38");
  });
});
