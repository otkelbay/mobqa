import { T, fonts, shadows } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
function ToastContainer({ toasts, onDismiss }) {
  return (
    <div
      className={css({
        position: 'fixed',
        top: 16,
        right: 16,
        zIndex: 9999,
        display: 'flex',
        flexDirection: 'column',
        gap: 8,
      })}
    >
      {toasts.map((t) => {
        const title = t.title || 'Notification';
        const type = t.type;
        return (
          <div
            key={t.id}
            className={css({
              width: 360,
              background: T.bgSurface,
              border: `0.5px solid ${T.borderDefault}`,
              borderLeft: `3px solid ${type === 'success' ? T.success : type === 'warning' ? T.warning : T.danger}`,
              borderRadius: 8,
              padding: '12px 16px',
              boxShadow: shadows.toast,
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'flex-start',
              animation: 'slideInRight 200ms ease-out',
            })}
          >
            <div>
              <p
                className={css({
                  margin: 0,
                  fontSize: 14,
                  fontWeight: 500,
                  fontFamily: fonts.body,
                  color: T.textPrimary,
                })}
              >
                {title}
              </p>
              {t.body && (
                <p
                  className={css({
                    margin: '2px 0 0',
                    fontSize: 13,
                    fontFamily: fonts.body,
                    color: T.textSecondary,
                  })}
                >
                  {t.body}
                </p>
              )}
            </div>
            <button
              onClick={() => onDismiss(t.id)}
              className={css({
                background: 'none',
                border: 'none',
                cursor: 'pointer',
                color: T.textTertiary,
                fontSize: 16,
                padding: 0,
                lineHeight: 1,
              })}
            >
              ×
            </button>
          </div>
        );
      })}
    </div>
  );
}
export { ToastContainer };
