import { badgeDotStyle, platformBadgeStyle, statusBadgeStyle, statusConfig } from '../../styles/recipes.js';
import { css } from '../../styles/styleRegistry.js';
import { platformLabel } from '../../services/viewModelMappers.js';
function StatusBadge({ status, size = 'md' }) {
  const c = statusConfig[status] || statusConfig.pending;
  return (
    <span className={css(statusBadgeStyle(c, size))}>
      <span className={css(badgeDotStyle(c, status === 'running' || status === 'authoring'))} />
      {c.label}
    </span>
  );
}
function PlatformBadge({ platform }) {
  return <span className={css(platformBadgeStyle())}>{platformLabel(platform)}</span>;
}
export { PlatformBadge, StatusBadge };
