// Every naming rule of the v0.2.0 id grammar. expected-ids.json was written by
// the v0.2.0 (Acorn) extractor; the TypeScript extractor must give the same.
export function declared(a, b = 1, ...rest) { return a; }
export default function () {}
const arrow = (x) => x;
const named = function inner() {};
let assigned;
assigned = function () {};
window.app.handler = () => {};
this.self = function () {};
obj[key].tail = () => {};
export const Hooks = {
  Chart: {
    mounted() { this.local = () => 1; },
    get size() { return 1; },
    set size(v) {},
    plain: function () {},
    arrowed: (a) => a,
    "quoted key"() {},
    42() {},
    [computed]() {},
  },
};
class Widget {
  constructor(a) {}
  update(first, ...rest) {}
  static make() {}
  field = () => {};
  #secret() {}
  get value() { return 1; }
  [dynamic]() {}
}
const Expr = class Named { m() {} };
const Anon = class { m() {} };
function defaults(a = () => {}, {b = function () {}} = {}) {}
function outerA() { function twice() {} }
function outerB() { function twice() {} }
const nest = { a: { b: { c() {} } } };
[1, 2].map(function (v) { return v; });
[1, 2].map((v) => v * 2);
(function iife() { (() => {})(); })();
(async () => {})();
async function asyncDecl() {}
function* gen() {}
const o = { "(top)"() {}, async am() {}, *gm() {} };
setTimeout(() => {}, 0);
consume({ "(top)"() {} });
