import React from 'react';
import { useNavigate } from 'react-router-dom';
import { useProject } from '../../context/ProjectContext.jsx';
import { T, fonts } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import { BackLink, Page } from '../../components/ui/primitives.jsx';
import { IconSparkle, IconSteps } from '../../components/ui/icons.jsx';

function Choice({ icon, title, children, primary, onClick }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={css({
        textAlign: 'left',
        background: T.bgSurface,
        border: `1px solid ${primary ? T.brandTintAlt : T.borderDefault}`,
        borderRadius: 14,
        padding: 20,
        cursor: 'pointer',
        fontFamily: fonts.body,
        ':hover': { borderColor: T.brand },
      })}
    >
      {icon}
      <div className={css({ fontWeight: 700, fontSize: 16, marginTop: 16, color: T.textPrimary })}>{title}</div>
      <p className={css({ color: T.textSecondary, fontSize: 13, lineHeight: 1.5, margin: '7px 0 0' })}>{children}</p>
    </button>
  );
}

function NewTestChoicePage() {
  const navigate = useNavigate();
  const { projectId } = useProject();
  const base = `/apps/${projectId}/tests`;
  return (
    <Page
      title="New test"
      subtitle="Either way you get plain-language steps that you can edit before the agent authors them."
      width={760}
      back={<BackLink onClick={() => navigate(base)}>Back to tests</BackLink>}
    >
      <div className={css({ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 })}>
        <Choice
          primary
          icon={<IconSparkle size={21} color={T.brand} />}
          title="Draft with the agent"
          onClick={() => navigate(`${base}/new/agent`)}
        >
          Describe what to cover. The model drafts a few tests from the brief and what it knows about the app.
        </Choice>
        <Choice
          icon={<IconSteps size={21} color={T.textSecondary} />}
          title="Write the steps"
          onClick={() => navigate(`${base}/new/manual`)}
        >
          Add steps one by one: an action, what it works on, and what should happen.
        </Choice>
      </div>
    </Page>
  );
}

export { NewTestChoicePage };
