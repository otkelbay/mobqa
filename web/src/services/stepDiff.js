// Diff between the current steps and an agent proposal.
// Rows drive the preview in the steps list: same | added | removed | changed.

function stepSignature(step = {}) {
  return [step.action, step.target, step.description, step.expectedResult]
    .map((value) =>
      String(value ?? '')
        .trim()
        .toLowerCase(),
    )
    .join('|');
}

// Pairs each run of removals immediately followed by additions into 'changed'
// rows (old→new), which is how single-step edits read naturally.
function pairChanges(rows) {
  const out = [];
  let k = 0;
  while (k < rows.length) {
    if (rows[k].type !== 'removed') {
      out.push(rows[k]);
      k += 1;
      continue;
    }
    const removed = [];
    while (k < rows.length && rows[k].type === 'removed') removed.push(rows[k++]);
    const added = [];
    while (k < rows.length && rows[k].type === 'added') added.push(rows[k++]);
    const pairs = Math.min(removed.length, added.length);
    for (let p = 0; p < pairs; p += 1) {
      out.push({
        type: 'changed',
        step: added[p].step,
        oldStep: removed[p].step,
        oldIndex: removed[p].oldIndex,
        newIndex: added[p].newIndex,
      });
    }
    for (let p = pairs; p < removed.length; p += 1) out.push(removed[p]);
    for (let p = pairs; p < added.length; p += 1) out.push(added[p]);
  }
  return out;
}

// LCS over step signatures.
function diffSteps(oldSteps = [], newSteps = []) {
  const a = oldSteps.map(stepSignature);
  const b = newSteps.map(stepSignature);
  const n = a.length;
  const m = b.length;
  const dp = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i -= 1) {
    for (let j = m - 1; j >= 0; j -= 1) {
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const rows = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      rows.push({ type: 'same', step: newSteps[j], oldIndex: i, newIndex: j });
      i += 1;
      j += 1;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      rows.push({ type: 'removed', step: oldSteps[i], oldIndex: i });
      i += 1;
    } else {
      rows.push({ type: 'added', step: newSteps[j], newIndex: j });
      j += 1;
    }
  }
  while (i < n) rows.push({ type: 'removed', step: oldSteps[i++], oldIndex: i - 1 });
  while (j < m) rows.push({ type: 'added', step: newSteps[j++], newIndex: j - 1 });
  return pairChanges(rows);
}

function diffSummary(rows = []) {
  const summary = { added: 0, removed: 0, changed: 0 };
  for (const row of rows) {
    if (row.type === 'added') summary.added += 1;
    else if (row.type === 'removed') summary.removed += 1;
    else if (row.type === 'changed') summary.changed += 1;
  }
  summary.total = summary.added + summary.removed + summary.changed;
  return summary;
}

export { diffSteps, diffSummary, stepSignature };
