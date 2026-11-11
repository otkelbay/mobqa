const registry = new Map();
let sheet;

const unitlessProperties = new Set([
  'animationIterationCount',
  'aspectRatio',
  'borderImageOutset',
  'borderImageSlice',
  'borderImageWidth',
  'boxFlex',
  'boxFlexGroup',
  'boxOrdinalGroup',
  'columnCount',
  'columns',
  'flex',
  'flexGrow',
  'flexPositive',
  'flexShrink',
  'flexNegative',
  'flexOrder',
  'gridArea',
  'gridRow',
  'gridRowEnd',
  'gridRowSpan',
  'gridRowStart',
  'gridColumn',
  'gridColumnEnd',
  'gridColumnSpan',
  'gridColumnStart',
  'fontWeight',
  'lineClamp',
  'lineHeight',
  'opacity',
  'order',
  'orphans',
  'scale',
  'tabSize',
  'widows',
  'zIndex',
  'zoom',
  'fillOpacity',
  'floodOpacity',
  'stopOpacity',
  'strokeDasharray',
  'strokeDashoffset',
  'strokeMiterlimit',
  'strokeOpacity',
  'strokeWidth',
]);

function ensureSheet() {
  if (sheet || typeof document === 'undefined') return sheet;
  const tag = document.createElement('style');
  tag.setAttribute('data-qa-style-registry', '');
  document.head.appendChild(tag);
  sheet = tag.sheet;
  return sheet;
}

function toKebab(property) {
  return property.replace(/[A-Z]/g, (match) => `-${match.toLowerCase()}`).replace(/^ms-/, '-ms-');
}

function toCssValue(property, value) {
  if (value == null || typeof value === 'boolean') return null;
  if (typeof value === 'number' && value !== 0 && !unitlessProperties.has(property)) {
    return `${value}px`;
  }
  return String(value);
}

function serialize(style) {
  return Object.entries(style)
    .filter(([, value]) => value != null && value !== false)
    .map(([property, value]) => {
      const cssValue = toCssValue(property, value);
      return cssValue == null ? '' : `${toKebab(property)}:${cssValue}`;
    })
    .filter(Boolean)
    .join(';');
}

function hash(input) {
  let h = 5381;
  for (let i = 0; i < input.length; i += 1) h = (h * 33) ^ input.charCodeAt(i);
  return (h >>> 0).toString(36);
}

function register(style, specificity = 'normal') {
  if (!style) return '';
  const serialized = serialize(style);
  if (!serialized) return '';
  const key = `${specificity}:${serialized}`;
  const existing = registry.get(key);
  if (existing) return existing;

  const className = `qac-${hash(key)}`;
  registry.set(key, className);
  const activeSheet = ensureSheet();
  const selector = specificity === 'low' ? `:where(.${className})` : `.${className}`;
  activeSheet?.insertRule(`${selector}{${serialized}}`, activeSheet.cssRules.length);
  return className;
}

function css(style) {
  return register(style);
}

function cssLow(style) {
  return register(style, 'low');
}

function cx(...classes) {
  return classes.flat().filter(Boolean).join(' ');
}

export { css, cssLow, cx };
