import {test} from 'node:test';
import assert from 'node:assert/strict';
import {searchRows} from '../search-model.js';

const e = (kind, label, full = label, repo = 'app') => ({id: `${kind}:${full}`, kind, label, full, repo});
const catalog = [
  e('module', 'Access', 'App.Access'),
  e('function', 'current_user/1', 'App.Access.current_user/1'),
  e('function', 'mount_current_scope/2', 'App.Live.mount_current_scope/2'),
  e('table', 'users'),
  e('context', 'Accounts'),
  e('service', 'app')
];
const ids = rows => rows.map(r => r.id);

test('a kind chip with an empty query lists entities of that kind (cartographer#21)', () => {
  assert.deepEqual(ids(searchRows(catalog, '', 'function')),
    ['function:App.Access.current_user/1', 'function:App.Live.mount_current_scope/2']);
  assert.deepEqual(ids(searchRows(catalog, '   ', 'table')), ['table:users']);
});

test('the All chip with an empty query lists only landmarks', () => {
  assert.deepEqual(ids(searchRows(catalog, '', 'all')).sort(),
    ['context:Accounts', 'module:App.Access', 'service:app', 'table:users']);
});

test('typed queries match every word and respect the chip', () => {
  assert.deepEqual(ids(searchRows(catalog, 'current_user', 'all')), ['function:App.Access.current_user/1']);
  assert.deepEqual(ids(searchRows(catalog, 'access current', 'function')), ['function:App.Access.current_user/1']);
  assert.deepEqual(ids(searchRows(catalog, 'current_user', 'module')), []);
});

test('an exact label ranks ahead of name order', () => {
  const rows = searchRows([e('function', 'list_users/0', 'App.Accounts.list_users/0'), e('table', 'users')], 'users', 'all');
  assert.deepEqual(ids(rows), ['table:users', 'function:App.Accounts.list_users/0']);
});
