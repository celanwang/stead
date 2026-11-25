const $ = id => document.getElementById(id);
const field = (form, name) => form.elements.namedItem(name);
const raw = (form, name) => field(form, name).value;
const read = (form, name) => raw(form, name).trim();
const path = value => encodeURIComponent(value);
const local = value => new Date(value).toLocaleString();
const inputTime = date => new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
const utc = value => {
  if (!value) throw new Error('Enter a date and time.');
  return new Date(value).toISOString();
};
const node = (tag, text, cls) => {
  const el = document.createElement(tag);
  el.textContent = text;
  if (cls) el.className = cls;
  return el;
};
const option = (value, text) => {
  const el = node('option', text);
  el.value = value;
  return el;
};
const randomID = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), n => n.toString(16).padStart(2, '0')).join('');
let connection = {token: '', abort: new AbortController()};
let selected = '', active = '', cursor = '', review, detail, tasks = [];

function message(text = '', error = false) {
  $('message').textContent = text;
  $('message').hidden = !text;
  $('message').toggleAttribute('data-error', error);
}

async function request(endpoint, body, key) {
  const session = connection;
  const headers = {Authorization: 'Bearer ' + session.token};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (key) headers['Idempotency-Key'] = key;
  const response = await fetch(endpoint, {
    method: body === undefined ? 'GET' : 'POST', headers, cache: 'no-store', credentials: 'omit',
    body: body === undefined ? undefined : JSON.stringify(body), signal: session.abort.signal,
  });
  const value = await response.json();
  if (session !== connection) throw new DOMException('Session changed', 'AbortError');
  if (!response.ok) throw new Error(value.clarification || value.error || 'Request failed');
  return value;
}

// An unchanged retry keeps the same ID even if the previous response never arrived.
function retryKey() {
  let last, id;
  return payload => {
    const current = JSON.stringify(payload);
    if (id === undefined || current !== last) {
      last = current;
      id = randomID();
    }
    return id;
  };
}
let createKey = retryKey(), eventKey = retryKey(), reviewKey = retryKey();

async function attempt(control, execute) {
  if (control.disabled) return;
  control.disabled = true;
  message();
  try { await execute(); }
  catch (error) { if (error.name !== 'AbortError') message(error.message, true); }
  finally { control.disabled = false; }
}
function form(id, execute) {
  $(id).addEventListener('submit', event => {
    event.preventDefault();
    attempt(event.submitter || $(id).querySelector('button'), () => execute($(id)));
  });
}

function show(screen) {
  active = screen;
  for (const id of ['connect', 'delegations', 'compose', 'reviews', 'tracking']) $(id).hidden = screen !== id;
  for (const button of $('nav').querySelectorAll('button')) {
    if (button.dataset.screen === screen) button.setAttribute('aria-current', 'page');
    else button.removeAttribute('aria-current');
  }
  $('nav').hidden = $('session').hidden = screen === 'connect';
  message();
}
function disconnect() {
  connection.abort.abort();
  connection = {token: '', abort: new AbortController()};
  selected = cursor = '';
  detail = review = undefined;
  tasks = [];
  createKey = retryKey();
  eventKey = retryKey();
  reviewKey = retryKey();
  for (const f of document.forms) f.reset();
  for (const id of ['task-list', 'task-details', 'review-items', 'notifications', 'principal', 'review-title']) $(id).replaceChildren();
  $('task-title').textContent = 'Select a delegation';
  $('event').hidden = $('review-details').hidden = $('notifications').hidden = true;
  for (const select of ['kind', 'grant', 'take_over']) field($('event'), select).replaceChildren();
  field($('record-review'), 'source').replaceChildren();
  setType();
  show('connect');
}
form('login', async f => {
  connection.token = read(f, 'token');
  try {
    const session = await request('/v1/session');
    $('principal').textContent = session.principal;
    f.reset();
    show('delegations');
    await loadList(true);
  } catch (error) {
    if (active === 'connect' && error.name !== 'AbortError') connection.token = '';
    throw error;
  }
});
$('disconnect').onclick = disconnect;
document.querySelector('.brand').onclick = event => {
  if (active === 'connect') return;
  event.preventDefault();
  show('delegations');
  attempt($('refresh'), () => loadList(true));
};
for (const button of $('nav').querySelectorAll('button')) {
  button.onclick = () => {
    show(button.dataset.screen);
    if (active === 'delegations') attempt(button, () => loadList(true));
  };
}

function labelOf(d) {
  if (d.Result) return d.Result.Complete ? 'Complete' : 'Returned';
  if (d.Wait?.Kind === 'approval') return 'Awaiting approval';
  if (d.Wait?.Kind === 'review') return 'Awaiting review';
  return d.Wait ? 'Waiting' : 'In progress';
}
function renderList() {
  const children = tasks.map(d => {
    const button = node('button', d.Goal, 'task-row');
    button.append(node('small', labelOf(d) + ' · ' + local(d.Deadline)));
    if (d.ID === selected) button.setAttribute('aria-current', 'true');
    button.onclick = () => attempt(button, () => openTask(d.ID, true));
    return button;
  });
  $('task-list').replaceChildren(...(children.length ? children : [node('p', 'No delegations yet.', 'muted')]));
  $('load-more').hidden = !cursor;
}
async function loadList(reset) {
  const page = await request('/v1/delegations' + (reset || !cursor ? '' : '?after=' + path(cursor)));
  if (reset) tasks = [];
	const known = new Set(tasks.map(d => d.ID));
	tasks.push(...page.items.map(item => item.delegation).filter(d => !known.has(d.ID)));
  cursor = page.next || '';
  renderList();
}
$('load-more').onclick = () => attempt($('load-more'), () => loadList(false));
$('refresh').onclick = () => attempt($('refresh'), async () => {
  await loadList(true);
  if (selected) await openTask(selected, true);
});

function facts(pairs) {
  const dl = node('dl', '');
  for (const [key, value] of pairs) {
    if (value) dl.append(node('dt', key), node('dd', value));
  }
  return dl;
}
function drawTask(result) {
  const {Delegation: d, Actions, Grants, Events} = result.state;
  $('task-title').textContent = d.Goal;
  const content = d.Request.Content || d.Briefing?.Draft || d.Result?.Draft;
  const children = [facts([
    ['Status', labelOf(d)], ['Deadline', local(d.Deadline)], ['Recipient', d.Request.Target], ['Delegation ID', d.ID],
    ['Waiting for', d.Wait?.Sender], ['Decision needed', d.Briefing?.AskReason], ['Result', d.Result?.Reason], ['Evidence', d.Result?.Evidence],
  ])];
  if (content) children.push(node('h3', d.Briefing ? 'Briefing' : 'Proposed message'), node('pre', content));
  for (const action of (Actions || []).slice(-5).reverse()) {
    children.push(facts([['Delivery', action.State], ['Action ID', action.ID], ['Receipt', action.Receipt?.Detail]]));
  }
  if (Events?.length) {
    const history = node('details', '');
    history.append(node('summary', 'Recent events'));
    for (const {Event, Processed} of Events.slice(-10).reverse()) {
      history.append(facts([['Event', Event.Kind], ['Sender', Event.Sender], ['State', Processed ? 'Processed' : 'Queued'], ['Original wording', Event.Raw]]));
    }
    children.push(history);
  }
  $('task-details').replaceChildren(...children);
  const f = $('event');
  f.hidden = Boolean(d.Result);
  if (f.hidden) return;
  f.reset();
  const grants = (Grants || []).filter(g => !g.RevokedAt && !g.InvalidatedBy && Date.parse(g.Terms.ExpiresAt) > Date.now());
  const kinds = [option('', 'Choose an action')];
  if (d.Wait?.Kind === 'approval' && d.Request.Content) kinds.push(option('approval', 'Approve the current scope'));
  kinds.push(option('input', 'Add instructions'));
  if (grants.length) kinds.push(option('revoke', 'Revoke authorization'));
  kinds.push(d.Briefing ? option('amend', 'Amend or take over an item') : option('evidence', 'Report completion evidence'));
  field(f, 'kind').replaceChildren(...kinds);
  field(f, 'grant').replaceChildren(...grants.map(g => option(g.ID, g.Terms.Scope.Target + ' · ' + g.ID)));
  field(f, 'take_over').replaceChildren(option('', 'No item'), ...(d.Briefing?.Items || [])
    .filter(item => !item.TakenOver).map(item => option(item.ID, item.ID)));
  field(f, 'expires').value = inputTime(new Date(Date.now() + 15 * 60_000));
  showEventFields();
}
let taskRequest = 0, taskBusy = 0;
function editingDecision() {
  const f = $('event');
  return !f.hidden && (f.contains(document.activeElement) || read(f, 'kind') || read(f, 'text'));
}
async function openTask(id, reset) {
  const sequence = ++taskRequest;
  taskBusy++;
  let result;
  try { result = await request('/v1/delegations/' + path(id)); }
  finally { taskBusy--; }
  if (sequence !== taskRequest || (!reset && (active !== 'delegations' || editingDecision()))) return;
  if (!reset && detail?.state.Delegation.Version === result.state.Delegation.Version) return;
  if (reset && id !== selected) eventKey = retryKey();
  selected = id;
  detail = result;
  const d = result.state.Delegation;
  const existing = tasks.findIndex(item => item.ID === d.ID);
  if (existing < 0) tasks.unshift(d);
  else tasks[existing] = d;
  renderList();
  drawTask(result);
}
function showEventFields() {
  const kind = $('event-kind').value;
  for (const el of $('event').querySelectorAll('[data-event]')) el.hidden = el.dataset.event !== kind;
}
$('event-kind').onchange = showEventFields;
form('event', async f => {
  const d = detail.state.Delegation;
  const kind = read(f, 'kind');
  const body = {kind, text: raw(f, 'text')};
  if (kind === 'approval') {
    body.correlation = d.Wait.Correlation;
    body.scope_fingerprint = detail.scope_fingerprint;
    body.expires_at = utc(read(f, 'expires'));
  }
  if (kind === 'revoke') body.grant_id = read(f, 'grant');
  if (kind === 'amend') body.amendment = {target: read(f, 'target'), format: read(f, 'format'), take_over: read(f, 'take_over')};
  body.id = eventKey(body);
  await request('/v1/delegations/' + path(d.ID) + '/events', body);
  eventKey = retryKey();
  await openTask(d.ID, true);
  message('Decision recorded.');
});

function setType() {
  const isBriefing = $('task-kind').value === 'briefing';
  $('briefing-fields').hidden = $('briefing-fields').disabled = !isBriefing;
  $('schedule-fields').hidden = $('schedule-fields').disabled = isBriefing;
}
$('task-kind').onchange = setType;
form('create', async f => {
  const body = {goal: read(f, 'goal'), deadline: utc(read(f, 'deadline'))};
  const target = read(f, 'target');
  if (read(f, 'type') === 'briefing') {
    body.briefing = {
      target, project: read(f, 'project'), reviewer: read(f, 'reviewer'),
      audience: read(f, 'audience'), sources: read(f, 'sources').split(',').map(s => s.trim()).filter(Boolean),
      format: read(f, 'format'), fallback: field(f, 'fallback').checked,
    };
  } else {
    body.scheduling = {target, content: raw(f, 'content')};
    if (read(f, 'resource') || read(f, 'start') || read(f, 'end')) {
      if (!read(f, 'resource')) throw new Error('Enter the resource key for this interval.');
      body.scheduling.resources = [{key: read(f, 'resource'), start: utc(read(f, 'start')), end: utc(read(f, 'end'))}];
    }
  }
  const result = await request('/v1/delegations', body, createKey(body));
  createKey = retryKey();
  f.reset();
  setType();
  show('delegations');
  await loadList(true);
  await openTask(result.delegation.ID, true);
  message('Delegation submitted.');
});
form('draft', async f => {
  const draft = await request('/v1/drafts', {text: read(f, 'text')});
  if (draft.kind === 'tracking') {
    message('This is a tracking request. Connect its source collector and open Notifications.');
    return;
  }
  const create = $('create');
  $('task-kind').value = draft.kind;
  setType();
  field(create, 'goal').value = draft.goal;
  if (draft.kind === 'scheduling') {
    field(create, 'target').value = draft.scheduling.target;
    field(create, 'content').value = draft.scheduling.content;
  } else {
    field(create, 'sources').value = draft.briefing.sources.join(', ');
    field(create, 'format').value = draft.briefing.format;
  }
  field(create, 'goal').focus();
  message('Draft ready for your review.');
});

async function openReview(id) {
  const result = await request('/v1/reviews/' + path(id));
  review = result;
  $('review-title').textContent = result.goal;
  $('review-details').hidden = false;
  const items = result.items.map(item => facts([
    ['Source', item.source], ['Fingerprint', item.fingerprint], ['Excerpt', item.rendered], ['Status', item.reviewed ? 'Reviewed' : 'Pending'],
  ]));
  $('review-items').replaceChildren(facts([['Deadline', local(result.deadline)]]), ...items);
  if (!items.length) $('review-items').append(node('p', 'No current source fingerprints.', 'muted'));
  const pending = result.items.filter(item => !item.reviewed);
  field($('record-review'), 'source').replaceChildren(...pending.map(item => option(item.source, item.source)));
  $('record-review').hidden = result.closed || !pending.length;
}
form('open-review', f => {
  $('review-details').hidden = true;
  review = undefined;
  return openReview(read(f, 'id'));
});
form('record-review', async f => {
  const item = review.items.find(value => value.source === read(f, 'source'));
  if (!item) throw new Error('Open the current review again.');
  const body = {kind: 'review', text: raw(f, 'text'), review: {source: item.source, fingerprint: item.fingerprint}};
  body.id = reviewKey([review.id, body]);
  await request('/v1/delegations/' + path(review.id) + '/events', body);
  f.reset();
  await openReview(review.id);
  message('Review recorded. The worker will match it to the source fingerprint.');
});

async function openFeed(project) {
  const prefix = '/v1/projects/' + path(project) + '/feed';
  const items = await request(prefix);
  const cards = (items || []).map(item => {
    const card = node('article', '', 'entry');
    const observation = item.Observation;
    const button = node('button', 'Mark read');
    button.onclick = () => attempt(button, async () => {
      await request(prefix + '/' + item.Seq + '/ack', {});
      await openFeed(project);
    });
    card.append(node('h3', observation.Title || observation.Key.ObjectID),
      node('p', item.Decision.Disposition + ' · ' + observation.Key.Source + ' · version ' + observation.Revision, 'muted'),
      node('p', observation.Summary || ''), button);
    return card;
  });
  $('notifications').replaceChildren(...(cards.length ? cards : [node('p', 'No unread notifications.', 'muted')]));
  $('notifications').hidden = false;
}
form('open-feed', f => openFeed(read(f, 'project')));
window.setInterval(() => {
  if (document.hidden || active !== 'delegations' || !selected || taskBusy || editingDecision() || $('refresh').disabled) return;
  openTask(selected, false).catch(() => {});
}, 6000);
show('connect');
