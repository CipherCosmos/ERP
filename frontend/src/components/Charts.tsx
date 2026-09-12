import React from 'react';

const PALETTE = ['#1976d2', '#4caf50', '#ff9800', '#e91e63', '#9c27b0', '#00bcd4', '#ff5722', '#607d8b'];

/* ------------------------------------------------------------------ */
/*  StatCard                                                          */
/* ------------------------------------------------------------------ */

export interface StatCardProps {
  title: string;
  value: string | number;
  subtitle?: string;
  color?: string;
}

export function StatCard({ title, value, subtitle, color = '#1976d2' }: StatCardProps) {
  return (
    <div style={{
      background: 'white',
      borderRadius: '8px',
      boxShadow: '0 1px 3px rgba(0,0,0,0.1)',
      display: 'flex',
      overflow: 'hidden',
    }}>
      <div style={{ width: 4, background: color, flexShrink: 0 }} />
      <div style={{ padding: '1.25rem 1rem' }}>
        <div style={{ fontSize: '0.8rem', color: '#888', marginBottom: '0.35rem', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
          {title}
        </div>
        <div style={{ fontSize: '1.5rem', fontWeight: 700, color: '#222' }}>
          {value}
        </div>
        {subtitle && (
          <div style={{ fontSize: '0.75rem', color: '#999', marginTop: '0.25rem' }}>
            {subtitle}
          </div>
        )}
      </div>
    </div>
  );
}

/* ------------------------------------------------------------------ */
/*  BarChart                                                          */
/* ------------------------------------------------------------------ */

export interface BarChartProps {
  data: { label: string; value: number; color?: string }[];
  height?: number;
  width?: number;
}

export function BarChart({ data, height = 220, width = 500 }: BarChartProps) {
  if (!data || data.length === 0) {
    return (
      <div style={{ padding: '2rem', textAlign: 'center', color: '#999' }}>
        No data available
      </div>
    );
  }

  const maxVal = Math.max(...data.map(d => d.value), 1);
  const padding = { top: 25, right: 10, bottom: 45, left: 10 };
  const chartW = width - padding.left - padding.right;
  const chartH = height - padding.top - padding.bottom;
  const barGap = 8;

  // A bar's width is capped, not simply the available space divided by the
  // number of bars. Without the cap a single category produces one bar 464px
  // wide — a solid block filling the plot area, which reads as a broken chart
  // rather than as a chart of one thing. Shops routinely have one or two
  // categories when they start, so this is the first thing they see.
  const MAX_BAR_W = 56;
  const barW = Math.min(MAX_BAR_W, Math.max(4, (chartW - barGap * (data.length + 1)) / data.length));

  // Centre the bars when they no longer fill the width, so a chart of two sits
  // in the middle instead of hugging the left edge.
  const groupW = data.length * barW + barGap * (data.length - 1);
  const startX = padding.left + Math.max(barGap, (chartW - groupW) / 2);

  // Rotate the axis labels only when they would otherwise collide. At four bars
  // or fewer there is room to set them straight, and a horizontal label is far
  // easier to read than one at 25 degrees.
  const rotateLabels = data.length > 4 || barW < 40;

  return (
    <div style={{ width: '100%', overflowX: 'auto' }}>
      <svg
        viewBox={`0 0 ${width} ${height}`}
        style={{ width: '100%', maxWidth: width, height: 'auto', display: 'block', margin: '0 auto' }}
        preserveAspectRatio="xMidYMid meet"
      >
        {/* grid lines */}
        {[0, 0.25, 0.5, 0.75, 1].map(frac => {
          const y = padding.top + chartH * (1 - frac);
          return (
            <line
              key={frac}
              x1={padding.left}
              x2={padding.left + chartW}
              y1={y}
              y2={y}
              stroke="#eee"
              strokeWidth={1}
            />
          );
        })}

        {data.map((d, i) => {
          const barH = (d.value / maxVal) * chartH;
          const x = startX + i * (barW + barGap);
          const y = padding.top + chartH - barH;
          const fill = d.color || PALETTE[i % PALETTE.length];

          return (
            <g key={i}>
              <rect
                x={x}
                y={y}
                width={barW}
                height={barH}
                rx={3}
                fill={fill}
              />
              {/* value label above bar */}
              <text
                x={x + barW / 2}
                y={y - 5}
                textAnchor="middle"
                fontSize={10}
                fill="#555"
              >
                {d.value >= 1000 ? `${(d.value / 1000).toFixed(1)}k` : d.value.toFixed(0)}
              </text>
              {/* x-axis label */}
              <text
                x={x + barW / 2}
                y={padding.top + chartH + 14}
                textAnchor="middle"
                fontSize={rotateLabels ? 9 : 11}
                fill="#64748b"
                transform={
                  rotateLabels
                    ? `rotate(-25, ${x + barW / 2}, ${padding.top + chartH + 14})`
                    : undefined
                }
              >
                {d.label.length > 16 ? d.label.slice(0, 15) + '\u2026' : d.label}
              </text>
            </g>
          );
        })}
      </svg>
    </div>
  );
}

/* ------------------------------------------------------------------ */
/*  PieChart                                                          */
/* ------------------------------------------------------------------ */

export interface PieChartProps {
  data: { label: string; value: number; color?: string }[];
  size?: number;
}

function polarToCartesian(cx: number, cy: number, r: number, angleDeg: number) {
  const rad = ((angleDeg - 90) * Math.PI) / 180;
  return { x: cx + r * Math.cos(rad), y: cy + r * Math.sin(rad) };
}

function describeArc(cx: number, cy: number, r: number, startAngle: number, endAngle: number) {
  const start = polarToCartesian(cx, cy, r, endAngle);
  const end = polarToCartesian(cx, cy, r, startAngle);
  const largeArc = endAngle - startAngle > 180 ? 1 : 0;
  return `M ${cx} ${cy} L ${start.x} ${start.y} A ${r} ${r} 0 ${largeArc} 0 ${end.x} ${end.y} Z`;
}

export function PieChart({ data, size = 200 }: PieChartProps) {
  if (!data || data.length === 0) {
    return (
      <div style={{ padding: '2rem', textAlign: 'center', color: '#999' }}>
        No data available
      </div>
    );
  }

  const total = data.reduce((s, d) => s + d.value, 0);
  if (total === 0) {
    return (
      <div style={{ padding: '2rem', textAlign: 'center', color: '#999' }}>
        All values are zero
      </div>
    );
  }

  const cx = size / 2;
  const cy = size / 2;
  const r = size / 2 - 4;

  // Single-value special case: draw a full circle
  const nonZero = data.filter(d => d.value > 0);
  if (nonZero.length === 1) {
    const idx = data.indexOf(nonZero[0]);
    return (
      <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '1rem' }}>
        <svg viewBox={`0 0 ${size} ${size}`} style={{ width: size, maxWidth: '100%', height: 'auto' }}>
          <circle cx={cx} cy={cy} r={r} fill={nonZero[0].color || PALETTE[idx % PALETTE.length]} />
        </svg>
        <PieLegend data={data} total={total} />
      </div>
    );
  }

  let angle = 0;
  const slices = data
    .filter(d => d.value > 0)
    .map((d, i) => {
      const sliceAngle = (d.value / total) * 360;
      // clamp very tiny slices to at least 1 degree so they are visible
      const actualAngle = Math.max(sliceAngle, 1);
      const startAngle = angle;
      angle += actualAngle;
      const origIdx = data.indexOf(d);
      return (
        <path
          key={i}
          d={describeArc(cx, cy, r, startAngle, startAngle + actualAngle - 0.5)}
          fill={d.color || PALETTE[origIdx % PALETTE.length]}
          stroke="white"
          strokeWidth={2}
        />
      );
    });

  return (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '1rem' }}>
      <svg viewBox={`0 0 ${size} ${size}`} style={{ width: size, maxWidth: '100%', height: 'auto' }}>
        {slices}
      </svg>
      <PieLegend data={data} total={total} />
    </div>
  );
}

function PieLegend({ data, total }: { data: PieChartProps['data']; total: number }) {
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.5rem 1rem', justifyContent: 'center' }}>
      {data.map((d, i) => (
        <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: '0.8rem', color: '#555' }}>
          <span style={{
            width: 10,
            height: 10,
            borderRadius: 2,
            background: d.color || PALETTE[i % PALETTE.length],
            display: 'inline-block',
            flexShrink: 0,
          }} />
          {d.label} ({total > 0 ? ((d.value / total) * 100).toFixed(0) : 0}%)
        </div>
      ))}
    </div>
  );
}

/* ------------------------------------------------------------------ */
/*  LineChart                                                         */
/* ------------------------------------------------------------------ */

export interface LineChartProps {
  data: { label: string; value: number }[];
  height?: number;
  width?: number;
  color?: string;
}

export function LineChart({ data, height = 220, width = 500, color = '#1976d2' }: LineChartProps) {
  if (!data || data.length === 0) {
    return (
      <div style={{ padding: '2rem', textAlign: 'center', color: '#999' }}>
        No data available
      </div>
    );
  }

  const padding = { top: 15, right: 15, bottom: 45, left: 15 };
  const chartW = width - padding.left - padding.right;
  const chartH = height - padding.top - padding.bottom;
  const maxVal = Math.max(...data.map(d => d.value), 1);

  const points = data.map((d, i) => {
    const x = padding.left + (data.length === 1 ? chartW / 2 : (i / (data.length - 1)) * chartW);
    const y = padding.top + chartH - (d.value / maxVal) * chartH;
    return { x, y };
  });

  const polyline = points.map(p => `${p.x},${p.y}`).join(' ');

  // area fill under line
  const areaPath =
    `M ${points[0].x},${padding.top + chartH} ` +
    points.map(p => `L ${p.x},${p.y}`).join(' ') +
    ` L ${points[points.length - 1].x},${padding.top + chartH} Z`;

  return (
    <div style={{ width: '100%', overflowX: 'auto' }}>
      <svg
        viewBox={`0 0 ${width} ${height}`}
        style={{ width: '100%', maxWidth: width, height: 'auto', display: 'block', margin: '0 auto' }}
        preserveAspectRatio="xMidYMid meet"
      >
        {/* horizontal grid lines */}
        {[0, 0.25, 0.5, 0.75, 1].map(frac => {
          const y = padding.top + chartH * (1 - frac);
          return (
            <g key={frac}>
              <line x1={padding.left} x2={padding.left + chartW} y1={y} y2={y} stroke="#eee" strokeWidth={1} />
              <text x={padding.left - 2} y={y + 3} textAnchor="end" fontSize={8} fill="#bbb">
                {(maxVal * frac).toFixed(0)}
              </text>
            </g>
          );
        })}

        {/* shaded area */}
        <path d={areaPath} fill={color} opacity={0.08} />

        {/* line */}
        <polyline
          points={polyline}
          fill="none"
          stroke={color}
          strokeWidth={2}
          strokeLinejoin="round"
          strokeLinecap="round"
        />

        {/* dots and labels */}
        {points.map((p, i) => (
          <g key={i}>
            <circle cx={p.x} cy={p.y} r={3.5} fill="white" stroke={color} strokeWidth={2} />
            <text
              x={p.x}
              y={padding.top + chartH + 14}
              textAnchor="middle"
              fontSize={9}
              fill="#777"
              transform={`rotate(-25, ${p.x}, ${padding.top + chartH + 14})`}
            >
              {data[i].label.length > 8 ? data[i].label.slice(0, 7) + '\u2026' : data[i].label}
            </text>
          </g>
        ))}
      </svg>
    </div>
  );
}
