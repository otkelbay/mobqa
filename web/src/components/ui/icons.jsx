// Inline SVG icon set extracted from the MobQA design.
// Every icon inherits `currentColor` so callers control color via CSS `color`.
import React from 'react';

function Icon({ size = 18, viewBox = '0 0 18 18', children, ...rest }) {
  return (
    <svg width={size} height={size} viewBox={viewBox} fill="none" {...rest}>
      {children}
    </svg>
  );
}

const stroke = { stroke: 'currentColor', strokeWidth: 1.5, strokeLinecap: 'round', strokeLinejoin: 'round' };

export const IconTests = (p) => (
  <Icon {...p}>
    <rect x="2.5" y="2.5" width="13" height="13" rx="3" {...stroke} fill="none" />
    <path d="M5.5 7l1.3 1.3L9 6" {...stroke} />
    <path d="M11 7.2h2.2M5.5 11.5h6" {...stroke} />
  </Icon>
);

export const IconRuns = (p) => (
  <Icon {...p}>
    <circle cx="9" cy="9" r="6.5" {...stroke} fill="none" />
    <path d="M7.4 6.6l3.4 2.4-3.4 2.4V6.6z" fill="currentColor" />
  </Icon>
);

export const IconUploads = (p) => (
  <Icon {...p}>
    <path d="M3 11.5v1.8c0 .7.6 1.2 1.2 1.2h9.6c.6 0 1.2-.5 1.2-1.2v-1.8" {...stroke} />
    <path d="M9 11V3.5M6 6.2L9 3.2l3 3" {...stroke} />
  </Icon>
);

export const IconDevices = (p) => (
  <Icon {...p}>
    <rect x="5" y="2" width="8" height="14" rx="2" {...stroke} fill="none" />
    <path d="M8 13.5h2" {...stroke} />
  </Icon>
);

export const IconSettings = (p) => (
  <Icon {...p}>
    <circle cx="9" cy="9" r="2.3" {...stroke} fill="none" />
    <path
      d="M9 2.5v1.6M9 13.9v1.6M2.5 9h1.6M13.9 9h1.6M4.4 4.4l1.1 1.1M12.5 12.5l1.1 1.1M4.4 13.6l1.1-1.1M12.5 5.5l1.1-1.1"
      {...stroke}
    />
  </Icon>
);

export const IconBuilds = (p) => (
  <Icon {...p}>
    <path d="M9 2.4l5.5 3.1v6.9L9 15.5 3.5 12.4V5.5L9 2.4z" {...stroke} fill="none" />
    <path d="M3.6 5.6L9 8.7l5.4-3.1M9 8.7v6.7" {...stroke} />
  </Icon>
);

export const IconApis = (p) => (
  <Icon {...p}>
    <path d="M6.5 5.5L3 9l3.5 3.5M11.5 5.5L15 9l-3.5 3.5M10 3.5l-2 11" {...stroke} />
  </Icon>
);

export const IconData = (p) => (
  <Icon {...p}>
    <ellipse cx="9" cy="4.5" rx="5.5" ry="2.2" {...stroke} fill="none" />
    <path d="M3.5 4.5v9c0 1.2 2.5 2.2 5.5 2.2s5.5-1 5.5-2.2v-9M3.5 9c0 1.2 2.5 2.2 5.5 2.2s5.5-1 5.5-2.2" {...stroke} />
  </Icon>
);

export const IconMemory = (p) => (
  <Icon {...p}>
    <path
      d="M9 3.2c-1-.9-2.4-1-3.6-.4C4 3.4 3.3 4.8 3.6 6.2 2.7 6.8 2.3 8 2.7 9.1c.3 1 1.2 1.7 2.2 1.8.1 1.4 1.3 2.5 2.7 2.4 1 0 1.9-.6 2.4-1.5"
      {...stroke}
      fill="none"
    />
    <path
      d="M9 3.2c1-.9 2.4-1 3.6-.4 1.2.6 1.9 2 1.6 3.4.9.6 1.3 1.8.9 2.9-.3 1-1.2 1.7-2.2 1.8-.1 1.4-1.3 2.5-2.7 2.4-1 0-1.9-.6-2.4-1.5"
      {...stroke}
      fill="none"
    />
    <path d="M9 3.2v11.4" {...stroke} />
  </Icon>
);

export const IconSecrets = (p) => (
  <Icon {...p}>
    <circle cx="6" cy="7" r="3.2" {...stroke} fill="none" />
    <path d="M8.5 8.5l5 5M11.5 11.5l1.3-1.3M13 13l1.2-1.2" {...stroke} />
  </Icon>
);

export const IconSparkle = (p) => (
  <Icon {...p}>
    <path d="M9 2.5l1.6 4.1 4.4.3-3.4 2.8 1.1 4.3L9 11.8 5.3 14.3l1.1-4.3L3 7.2l4.4-.3L9 2.5z" {...stroke} />
  </Icon>
);

export const IconPlay = (p) => (
  <Icon {...p}>
    <path d="M4.5 3l7 4.5-7 4.5V3z" fill="currentColor" />
  </Icon>
);

export const IconCheck = (p) => (
  <Icon {...p}>
    <path d="M4 8.2l2.6 2.6L12 5.4" {...stroke} fill="none" />
  </Icon>
);

export const IconPlus = (p) => (
  <Icon {...p}>
    <path d="M9 3.5v11M3.5 9h11" {...stroke} />
  </Icon>
);

export const IconChevronRight = (p) => (
  <Icon {...p}>
    <path d="M6 4l4 4-4 4" {...stroke} fill="none" />
  </Icon>
);

export const IconChevronLeft = (p) => (
  <Icon {...p}>
    <path d="M9 3.5L5 7.5l4 4" {...stroke} fill="none" />
  </Icon>
);

export const IconClose = (p) => (
  <Icon {...p}>
    <path d="M4.5 4.5l9 9M13.5 4.5l-9 9" {...stroke} />
  </Icon>
);

export const IconText = (p) => (
  <Icon {...p} viewBox="0 0 20 20">
    <path d="M4.5 5.5h11M4.5 10h11M4.5 14.5h7" {...stroke} />
  </Icon>
);

export const IconVideo = (p) => (
  <Icon {...p} viewBox="0 0 20 20">
    <rect x="3" y="5.5" width="9" height="9" rx="2" {...stroke} fill="none" />
    <path d="M12 9l4-2.2v6.4L12 11" {...stroke} />
  </Icon>
);

export const IconPhoto = (p) => (
  <Icon {...p} viewBox="0 0 20 20">
    <rect x="3" y="4.5" width="14" height="11" rx="2.5" {...stroke} fill="none" />
    <circle cx="10" cy="10" r="2.6" {...stroke} fill="none" />
  </Icon>
);

export const IconCopy = (p) => (
  <Icon {...p}>
    <rect x="6" y="6" width="8.5" height="8.5" rx="2" {...stroke} fill="none" />
    <path d="M11.5 4.5H5.5A1.5 1.5 0 004 6v6" {...stroke} />
  </Icon>
);

export const IconMore = (p) => (
  <Icon {...p} viewBox="0 0 16 16">
    <circle cx="8" cy="3.5" r="1.3" fill="currentColor" />
    <circle cx="8" cy="8" r="1.3" fill="currentColor" />
    <circle cx="8" cy="12.5" r="1.3" fill="currentColor" />
  </Icon>
);

export const IconCode = (p) => (
  <Icon {...p}>
    <path d="M6.5 6L4 9l2.5 3M11.5 6L14 9l-2.5 3" {...stroke} fill="none" />
  </Icon>
);

export const IconSteps = (p) => (
  <Icon {...p}>
    <path
      d="M11 3.5H6a1.5 1.5 0 00-1.5 1.5v8A1.5 1.5 0 006 14.5h6a1.5 1.5 0 001.5-1.5V6L11 3.5z"
      {...stroke}
      fill="none"
    />
    <path d="M6.5 8.5h5M6.5 11h3.5" {...stroke} />
  </Icon>
);

export const IconLogo = ({ size = 15, ...rest }) => (
  <svg width={size} height={size} viewBox="0 0 15 15" fill="none" {...rest}>
    <path d="M3 11V4.5L7.5 8 12 4.5V11" stroke="#fff" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);
