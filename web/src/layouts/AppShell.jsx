import React from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useAppData } from '../context/AppDataContext.jsx';
import { useProject } from '../context/ProjectContext.jsx';
import { T, fonts } from '../styles/tokens.js';
import { css } from '../styles/styleRegistry.js';
import { Avatar } from '../components/ui/primitives.jsx';
import {
  IconApis,
  IconBuilds,
  IconChevronRight,
  IconDevices,
  IconMemory,
  IconMore,
  IconRuns,
  IconSecrets,
  IconSettings,
  IconTests,
} from '../components/ui/icons.jsx';

const SIDEBAR_WIDTH = 248;

const SECTIONS = [
  {
    title: 'WORK',
    items: [
      { id: 'tests', label: 'Tests', Icon: IconTests },
      { id: 'runs', label: 'Runs', Icon: IconRuns },
      { id: 'builds', label: 'Builds', Icon: IconBuilds },
      { id: 'devices', label: 'Devices', Icon: IconDevices },
    ],
  },
  {
    title: 'AGENT RESOURCES',
    items: [
      { id: 'knowledge', label: 'Knowledge', Icon: IconMemory },
      { id: 'actions', label: 'API actions', Icon: IconApis },
      { id: 'environments', label: 'Environments', Icon: IconSecrets },
    ],
  },
  { title: 'PROJECT', items: [{ id: 'settings', label: 'Settings', Icon: IconSettings }] },
];

function initialsOf(name, fallback = 'U') {
  const out = (name || '')
    .split(/\s+/)
    .filter(Boolean)
    .map((p) => p[0])
    .join('')
    .slice(0, 2)
    .toUpperCase();
  return out || fallback;
}

function NavItem({ item, active, onClick }) {
  const { Icon, label } = item;
  return (
    <button
      onClick={onClick}
      className={css({
        width: '100%',
        display: 'flex',
        alignItems: 'center',
        gap: 11,
        padding: '9px 10px',
        border: 'none',
        borderRadius: 9,
        cursor: 'pointer',
        fontFamily: fonts.body,
        fontSize: 13.5,
        fontWeight: 500,
        textAlign: 'left',
        marginBottom: 2,
        background: active ? '#F6F5FE' : 'transparent',
        color: active ? T.brandText : T.textMedium,
        transition: 'background 120ms, color 120ms',
      })}
    >
      <Icon size={18} />
      {label}
    </button>
  );
}

function SectionHeader({ children, first }) {
  return (
    <div
      className={css({
        fontSize: 10.5,
        fontWeight: 600,
        letterSpacing: '0.08em',
        color: T.textFaint,
        padding: first ? '10px 10px 6px' : '18px 10px 6px',
      })}
    >
      {children}
    </div>
  );
}

function AppShell({ children }) {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const { user } = useAppData();
  const { projectId, project } = useProject();

  const section = pathname.split('/')[3] || 'tests';
  const go = (id) => () => navigate(`/apps/${projectId}/${id}`);
  const userName = user?.name?.trim() || user?.email?.split('@')[0] || 'Account';

  return (
    <div
      className={css({
        display: 'flex',
        height: '100vh',
        width: '100%',
        overflow: 'hidden',
        background: T.bgBase,
        color: T.textPrimary,
      })}
    >
      <aside
        className={css({
          width: SIDEBAR_WIDTH,
          flexShrink: 0,
          background: T.bgSurface,
          borderRight: `1px solid ${T.borderDefault}`,
          display: 'flex',
          flexDirection: 'column',
          height: '100vh',
        })}
      >
        {/* Project switcher → back to projects */}
        <div className={css({ padding: '18px 18px 12px' })}>
          <button
            onClick={() => navigate('/')}
            className={css({
              display: 'flex',
              alignItems: 'center',
              gap: 9,
              width: '100%',
              background: T.bgSubtle,
              border: `1px solid ${T.borderDefault}`,
              borderRadius: 11,
              padding: '9px 10px',
              cursor: 'pointer',
              textAlign: 'left',
              fontFamily: fonts.body,
            })}
          >
            <Avatar
              initials={project?.initial || '?'}
              size={30}
              bg={project?.color}
              color={project?.textColor}
              square
            />
            <span className={css({ flex: 1, minWidth: 0 })}>
              <span
                className={css({
                  display: 'block',
                  fontSize: 13.5,
                  fontWeight: 600,
                  color: T.textPrimary,
                  whiteSpace: 'nowrap',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                })}
              >
                {project?.name || 'Project'}
              </span>
              <span className={css({ display: 'block', fontSize: 11, color: T.textTertiary, marginTop: 1 })}>
                {project?.platformLabel || ''}
              </span>
            </span>
            <span className={css({ display: 'flex', color: T.iconGray, transform: 'rotate(90deg)' })}>
              <IconChevronRight size={14} viewBox="0 0 14 14" />
            </span>
          </button>
        </div>

        <nav className={css({ flex: 1, padding: '6px 12px', overflowY: 'auto' })}>
          {SECTIONS.map((group, index) => (
            <React.Fragment key={group.title}>
              <SectionHeader first={index === 0}>{group.title}</SectionHeader>
              {group.items.map((item) => (
                <NavItem key={item.id} item={item} active={section === item.id} onClick={go(item.id)} />
              ))}
            </React.Fragment>
          ))}
        </nav>

        <button
          onClick={() => navigate('/profile')}
          className={css({
            padding: 12,
            borderTop: `1px solid ${T.borderLight}`,
            display: 'flex',
            alignItems: 'center',
            gap: 10,
            width: '100%',
            background: 'transparent',
            border: 'none',
            cursor: 'pointer',
            fontFamily: fonts.body,
            textAlign: 'left',
          })}
        >
          <Avatar initials={initialsOf(userName)} size={30} />
          <span className={css({ flex: 1, minWidth: 0 })}>
            <span
              className={css({
                display: 'block',
                fontSize: 13,
                fontWeight: 500,
                color: T.textPrimary,
                whiteSpace: 'nowrap',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
              })}
            >
              {userName}
            </span>
            <span className={css({ display: 'block', fontSize: 11, color: T.textTertiary })}>{user?.email}</span>
          </span>
          <span className={css({ display: 'flex', color: T.iconGray })}>
            <IconMore size={16} />
          </span>
        </button>
      </aside>

      <main className={css({ flex: 1, height: '100vh', overflowY: 'auto' })}>{children}</main>
    </div>
  );
}

export { AppShell };
