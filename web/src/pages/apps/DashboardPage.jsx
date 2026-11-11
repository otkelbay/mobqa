import React from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppData } from '../../context/AppDataContext.jsx';
import { T, fonts } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import { Avatar, IconChip } from '../../components/ui/primitives.jsx';
import { IconLogo, IconPlus } from '../../components/ui/icons.jsx';

function initialsOf(name, fallback = 'U') {
  return (
    (name || '')
      .split(/\s+/)
      .filter(Boolean)
      .map((p) => p[0])
      .join('')
      .slice(0, 2)
      .toUpperCase() || fallback
  );
}

function ProjectCard({ app, onOpen }) {
  const [hov, setHov] = React.useState(false);
  return (
    <button
      onClick={onOpen}
      onMouseEnter={() => setHov(true)}
      onMouseLeave={() => setHov(false)}
      className={css({
        display: 'block',
        width: '100%',
        textAlign: 'left',
        background: T.bgSurface,
        border: `1px solid ${hov ? T.brandBorder : T.borderDefault}`,
        borderRadius: 16,
        padding: 22,
        cursor: 'pointer',
        fontFamily: fonts.body,
        boxShadow: hov ? '0 6px 22px rgba(28,28,36,0.06)' : 'none',
        transition: 'border-color 120ms, box-shadow 120ms',
      })}
    >
      <div className={css({ display: 'flex', alignItems: 'center', gap: 13, marginBottom: 16 })}>
        <Avatar initials={app.initial} size={46} bg={app.color} color={app.textColor} square />
        <div className={css({ minWidth: 0 })}>
          <div
            className={css({
              fontSize: 17,
              fontWeight: 600,
              color: T.textPrimary,
              whiteSpace: 'nowrap',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
            })}
          >
            {app.name}
          </div>
          <div
            className={css({
              fontSize: 12.5,
              color: T.textTertiary,
              marginTop: 2,
              whiteSpace: 'nowrap',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
            })}
          >
            {app.platformLabel || 'No platform yet'}
          </div>
        </div>
      </div>
      <div className={css({ fontSize: 13, color: T.textMuted, lineHeight: 1.5, minHeight: 39 })}>
        {app.description || app.packageName || 'No description'}
      </div>
      <div className={css({ fontSize: 12, color: T.textTertiary, marginTop: 12 })}>Updated {app.updated}</div>
    </button>
  );
}

function DashboardPage() {
  const navigate = useNavigate();
  const { projects, user } = useAppData();

  const open = (id) => () => navigate(`/apps/${id}/tests`);

  return (
    <div>
      <header
        className={css({
          display: 'flex',
          alignItems: 'center',
          gap: 16,
          padding: '20px 48px',
          borderBottom: `1px solid ${T.borderDefault}`,
          background: T.bgSurface,
          position: 'sticky',
          top: 0,
          zIndex: 5,
        })}
      >
        <div className={css({ display: 'flex', alignItems: 'center', gap: 9 })}>
          <IconChip size={26} bg={T.brand} radius={7}>
            <IconLogo size={15} />
          </IconChip>
          <span className={css({ fontSize: 18, fontWeight: 700, letterSpacing: '-0.02em', color: T.textPrimary })}>
            mobqa
          </span>
        </div>
        <div className={css({ flex: 1 })} />
        <button
          onClick={() => navigate('/profile')}
          className={css({ border: 'none', background: 'none', cursor: 'pointer', padding: 0 })}
        >
          <Avatar initials={initialsOf(user?.name || user?.email)} size={34} />
        </button>
      </header>

      <div className={css({ maxWidth: 1080, margin: '0 auto', padding: '44px 48px 80px' })}>
        <div
          className={css({
            display: 'flex',
            alignItems: 'flex-end',
            justifyContent: 'space-between',
            marginBottom: 32,
          })}
        >
          <div>
            <h1
              className={css({ fontSize: 30, fontWeight: 700, letterSpacing: '-0.025em', margin: 0, color: T.heading })}
            >
              Projects
            </h1>
            <p className={css({ fontSize: 14.5, color: T.textSecondary, margin: '8px 0 0' })}>
              Describe your app's flows — the agent authors them on real devices and replays them on every build
            </p>
          </div>
          <button
            onClick={() => navigate('/apps/new')}
            className={css({
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              background: T.brand,
              color: '#fff',
              border: 'none',
              borderRadius: 10,
              padding: '12px 18px',
              fontFamily: fonts.body,
              fontSize: 14,
              fontWeight: 600,
              cursor: 'pointer',
              boxShadow: '0 1px 2px rgba(113,87,251,0.35)',
            })}
          >
            <IconPlus size={16} viewBox="0 0 16 16" />
            New project
          </button>
        </div>

        <div
          className={css({ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(330px, 1fr))', gap: 18 })}
        >
          {projects.map((app) => (
            <ProjectCard key={app.id} app={app} onOpen={open(app.id)} />
          ))}

          <button
            onClick={() => navigate('/apps/new')}
            className={css({
              display: 'flex',
              flexDirection: 'column',
              alignItems: 'center',
              justifyContent: 'center',
              gap: 10,
              minHeight: 170,
              background: 'transparent',
              border: `1.5px dashed ${T.borderStrong}`,
              borderRadius: 16,
              cursor: 'pointer',
              fontFamily: fonts.body,
              color: T.textTertiary,
            })}
          >
            <IconPlus size={22} viewBox="0 0 22 22" />
            <span className={css({ fontSize: 13.5, fontWeight: 500 })}>New project</span>
          </button>
        </div>
      </div>
    </div>
  );
}

export { DashboardPage };
