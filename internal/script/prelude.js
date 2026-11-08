// Prelude for Bruno scripts: console, test/expect (a chai subset), the
// req/res wrappers and the assert-block operators. The host provides
// __host (Go callbacks), __reqData (the send-time request, mutated in
// place) and optionally __resData.
'use strict';

var __results = [];  // test() outcomes, in call order
var __asserts = [];  // assert-block outcomes
var __pending = [];  // async test bodies still running

function __fmt(v) {
  if (typeof v === 'string') return JSON.stringify(v);
  if (v === undefined) return 'undefined';
  if (typeof v === 'function') return '[Function]';
  if (typeof v === 'number' && !isFinite(v)) return String(v);
  if (v instanceof RegExp) return String(v);
  try {
    var s = JSON.stringify(v);
    if (s === undefined) return String(v);
    return s.length > 200 ? s.slice(0, 200) + '…' : s;
  } catch (e) {
    return String(v);
  }
}

function __show(v) {
  if (typeof v === 'string') return v;
  if (v instanceof Error) return (v.name ? v.name + ': ' : '') + v.message;
  if (v === undefined) return 'undefined';
  if (typeof v === 'function') return '[Function]';
  try {
    var s = JSON.stringify(v, null, 2);
    return s === undefined ? String(v) : s;
  } catch (e) {
    return String(v);
  }
}

function __errMsg(e) {
  if (e && typeof e === 'object' && 'message' in e) {
    var n = e.name && e.name !== 'Error' && e.name !== 'AssertionError' ? e.name + ': ' : '';
    return n + e.message;
  }
  return String(e);
}

var console = (function () {
  function mk(level) {
    return function () {
      var parts = [];
      for (var i = 0; i < arguments.length; i++) parts.push(__show(arguments[i]));
      __host.log(level, parts.join(' '));
    };
  }
  return { log: mk('log'), info: mk('info'), warn: mk('warn'), error: mk('error'), debug: mk('debug') };
})();

function require(name) {
  throw new Error('require("' + name + '") 暂不支持');
}

// ---- deep equality and types ----

function __type(v) {
  if (v === null) return 'null';
  if (Array.isArray(v)) return 'array';
  if (v instanceof Date) return 'date';
  if (v instanceof RegExp) return 'regexp';
  if (v instanceof Error) return 'error';
  return typeof v;
}

function __deepEq(a, b) {
  if (a === b) return a !== 0 || 1 / a === 1 / b;
  if (a !== a && b !== b) return true; // NaN
  var ta = __type(a), tb = __type(b);
  if (ta !== tb) return false;
  if (ta === 'date') return a.getTime() === b.getTime();
  if (ta === 'regexp') return String(a) === String(b);
  if (ta === 'array') {
    if (a.length !== b.length) return false;
    for (var i = 0; i < a.length; i++) if (!__deepEq(a[i], b[i])) return false;
    return true;
  }
  if (ta === 'object') {
    var ka = Object.keys(a), kb = Object.keys(b);
    if (ka.length !== kb.length) return false;
    for (var j = 0; j < ka.length; j++) {
      if (!Object.prototype.hasOwnProperty.call(b, ka[j])) return false;
      if (!__deepEq(a[ka[j]], b[ka[j]])) return false;
    }
    return true;
  }
  return false;
}

// ---- expect ----

class AssertionError extends Error {
  constructor(message) {
    super(message);
    this.name = 'AssertionError';
  }
}

function Assertion(obj, msg) {
  this._obj = obj;
  this._msg = msg;
  this._not = false;
  this._deep = false;
  this._own = false;
  this._contains = false;
  this._any = false;
  this._len = false;
}

(function (P) {
  P._check = function (ok, msg, notMsg) {
    if (this._not) ok = !ok;
    if (!ok) throw new AssertionError((this._msg ? this._msg + ': ' : '') + (this._not ? notMsg : msg));
    return this;
  };
  P._subject = function () {
    if (!this._len) return this._obj;
    var o = this._obj;
    if (o == null || typeof o.length !== 'number') {
      throw new AssertionError('expected ' + __fmt(o) + ' to have a length');
    }
    return o.length;
  };
  function getter(name, fn) {
    Object.defineProperty(P, name, { get: fn, configurable: true });
  }
  ['to', 'be', 'been', 'is', 'that', 'which', 'and', 'has', 'have', 'with', 'at', 'of', 'same', 'but', 'does', 'still', 'also']
    .forEach(function (w) { getter(w, function () { return this; }); });
  getter('not', function () { this._not = !this._not; return this; });
  getter('deep', function () { this._deep = true; return this; });
  getter('own', function () { this._own = true; return this; });
  getter('any', function () { this._any = true; return this; });
  getter('all', function () { this._any = false; return this; });

  // A chainable method works both as expect(x).to.include(y) and as a
  // flag, expect(o).to.include.keys('a'): the getter returns a function
  // whose prototype is the assertion, so chaining continues from it.
  function chainable(names, method, flag) {
    names.forEach(function (name) {
      getter(name, function () {
        var self = this;
        if (flag) flag.call(self);
        var f = function () { return method.apply(self, arguments); };
        Object.setPrototypeOf(f, self);
        return f;
      });
    });
  }
  function method(names, fn) {
    names.forEach(function (n) { P[n] = fn; });
  }

  var o = function (a) { return a._obj; };
  getter('ok', function () { return this._check(!!o(this), 'expected ' + __fmt(o(this)) + ' to be truthy', 'expected ' + __fmt(o(this)) + ' to be falsy'); });
  getter('true', function () { return this._check(o(this) === true, 'expected ' + __fmt(o(this)) + ' to be true', 'expected value not to be true'); });
  getter('false', function () { return this._check(o(this) === false, 'expected ' + __fmt(o(this)) + ' to be false', 'expected value not to be false'); });
  getter('null', function () { return this._check(o(this) === null, 'expected ' + __fmt(o(this)) + ' to be null', 'expected value not to be null'); });
  getter('undefined', function () { return this._check(o(this) === undefined, 'expected ' + __fmt(o(this)) + ' to be undefined', 'expected value not to be undefined'); });
  getter('NaN', function () { return this._check(Number.isNaN(o(this)), 'expected ' + __fmt(o(this)) + ' to be NaN', 'expected value not to be NaN'); });
  getter('exist', function () { return this._check(o(this) != null, 'expected ' + __fmt(o(this)) + ' to exist', 'expected ' + __fmt(o(this)) + ' to not exist'); });
  getter('empty', function () {
    var v = o(this), empty;
    if (typeof v === 'string' || Array.isArray(v)) empty = v.length === 0;
    else if (v && typeof v === 'object') empty = Object.keys(v).length === 0;
    else throw new AssertionError('.empty was passed non-string primitive ' + __fmt(v));
    return this._check(empty, 'expected ' + __fmt(v) + ' to be empty', 'expected ' + __fmt(v) + ' not to be empty');
  });

  method(['equal', 'equals', 'eq'], function (v) {
    var a = this._subject();
    var ok = this._deep ? __deepEq(a, v) : a === v;
    return this._check(ok, 'expected ' + __fmt(a) + ' to equal ' + __fmt(v), 'expected ' + __fmt(a) + ' to not equal ' + __fmt(v));
  });
  method(['eql', 'eqls'], function (v) {
    var a = this._subject();
    return this._check(__deepEq(a, v), 'expected ' + __fmt(a) + ' to deeply equal ' + __fmt(v), 'expected ' + __fmt(a) + ' to not deeply equal ' + __fmt(v));
  });
  function cmp(names, test, word) {
    method(names, function (n) {
      var a = this._subject();
      var what = this._len ? 'length ' + a : __fmt(a);
      return this._check(test(a, n), 'expected ' + what + ' to be ' + word + ' ' + __fmt(n), 'expected ' + what + ' to not be ' + word + ' ' + __fmt(n));
    });
  }
  cmp(['above', 'gt', 'greaterThan'], function (a, n) { return a > n; }, 'above');
  cmp(['least', 'gte', 'greaterThanOrEqual'], function (a, n) { return a >= n; }, 'at least');
  cmp(['below', 'lt', 'lessThan'], function (a, n) { return a < n; }, 'below');
  cmp(['most', 'lte', 'lessThanOrEqual'], function (a, n) { return a <= n; }, 'at most');
  method(['within'], function (lo, hi) {
    var a = this._subject();
    return this._check(a >= lo && a <= hi, 'expected ' + __fmt(a) + ' to be within ' + lo + '..' + hi, 'expected ' + __fmt(a) + ' to not be within ' + lo + '..' + hi);
  });
  method(['closeTo', 'approximately'], function (v, d) {
    var a = o(this);
    return this._check(Math.abs(a - v) <= d, 'expected ' + __fmt(a) + ' to be close to ' + v + ' +/- ' + d, 'expected ' + __fmt(a) + ' not to be close to ' + v + ' +/- ' + d);
  });
  chainable(['a', 'an'], function (t) {
    var v = o(this), actual = __type(v);
    t = String(t).toLowerCase();
    return this._check(actual === t, 'expected ' + __fmt(v) + ' to be a ' + t, 'expected ' + __fmt(v) + ' not to be a ' + t);
  });
  method(['instanceof', 'instanceOf'], function (C) {
    var v = o(this);
    return this._check(v instanceof C, 'expected ' + __fmt(v) + ' to be an instance of ' + (C && C.name), 'expected ' + __fmt(v) + ' to not be an instance of ' + (C && C.name));
  });
  chainable(['include', 'includes', 'contain', 'contains'], function (x) {
    var v = o(this), ok, self = this;
    if (typeof v === 'string') ok = v.indexOf(String(x)) >= 0;
    else if (Array.isArray(v)) ok = v.some(function (e) { return self._deep ? __deepEq(e, x) : e === x; });
    else if (v && typeof v === 'object' && x && typeof x === 'object') {
      ok = Object.keys(x).every(function (k) { return k in v && (self._deep ? __deepEq(v[k], x[k]) : v[k] === x[k]); });
    } else if (v && typeof v === 'object') ok = x in v;
    else throw new AssertionError('expected ' + __fmt(v) + ' to be a string, array or object');
    return this._check(ok, 'expected ' + __fmt(v) + ' to include ' + __fmt(x), 'expected ' + __fmt(v) + ' to not include ' + __fmt(x));
  }, function () { this._contains = true; });
  chainable(['length', 'lengthOf'], function (n) {
    var v = o(this);
    var len = v == null ? undefined : (v instanceof Map || v instanceof Set ? v.size : v.length);
    return this._check(len === n, 'expected ' + __fmt(v) + ' to have a length of ' + n + ' but got ' + len, 'expected ' + __fmt(v) + ' to not have a length of ' + n);
  }, function () { this._len = true; });
  method(['property', 'ownProperty', 'haveOwnProperty'], function (name, val) {
    var v = o(this);
    var has = v != null && (this._own ? Object.prototype.hasOwnProperty.call(Object(v), name) : name in Object(v));
    if (arguments.length > 1) {
      var got = has ? v[name] : undefined;
      var eq = has && (this._deep ? __deepEq(got, val) : got === val);
      this._check(eq, 'expected ' + __fmt(v) + ' to have property ' + __fmt(name) + ' of ' + __fmt(val) + ', but got ' + __fmt(got),
        'expected ' + __fmt(v) + ' to not have property ' + __fmt(name) + ' of ' + __fmt(val));
    } else {
      this._check(has, 'expected ' + __fmt(v) + ' to have property ' + __fmt(name), 'expected ' + __fmt(v) + ' to not have property ' + __fmt(name));
    }
    if (has && !this._not) {
      var next = new Assertion(v[name], this._msg);
      return next;
    }
    return this;
  });
  method(['keys', 'key'], function () {
    var v = o(this);
    var want = arguments.length === 1 && Array.isArray(arguments[0]) ? arguments[0] :
      (arguments.length === 1 && arguments[0] && typeof arguments[0] === 'object' ? Object.keys(arguments[0]) : Array.prototype.slice.call(arguments));
    var have = v && typeof v === 'object' ? Object.keys(v) : [];
    var ok;
    if (this._any) ok = want.some(function (k) { return have.indexOf(String(k)) >= 0; });
    else {
      ok = want.every(function (k) { return have.indexOf(String(k)) >= 0; });
      if (ok && !this._contains) ok = have.length === want.length;
    }
    return this._check(ok, 'expected ' + __fmt(v) + ' to have keys ' + __fmt(want), 'expected ' + __fmt(v) + ' to not have keys ' + __fmt(want));
  });
  method(['match', 'matches'], function (re) {
    var v = o(this);
    return this._check(re.test(v), 'expected ' + __fmt(v) + ' to match ' + re, 'expected ' + __fmt(v) + ' not to match ' + re);
  });
  method(['string'], function (s) {
    var v = o(this);
    return this._check(typeof v === 'string' && v.indexOf(s) >= 0, 'expected ' + __fmt(v) + ' to contain ' + __fmt(s), 'expected ' + __fmt(v) + ' to not contain ' + __fmt(s));
  });
  method(['oneOf'], function (list) {
    var v = o(this), self = this;
    var ok = list.some(function (e) { return self._deep ? __deepEq(e, v) : e === v; });
    return this._check(ok, 'expected ' + __fmt(v) + ' to be one of ' + __fmt(list), 'expected ' + __fmt(v) + ' to not be one of ' + __fmt(list));
  });
  method(['members'], function (list) {
    var v = o(this);
    var has = function (arr, x) { return arr.some(function (e) { return __deepEq(e, x); }); };
    var ok = Array.isArray(v) && list.every(function (x) { return has(v, x); }) &&
      (this._contains || (v.length === list.length && v.every(function (x) { return has(list, x); })));
    return this._check(ok, 'expected ' + __fmt(v) + ' to have the same members as ' + __fmt(list), 'expected ' + __fmt(v) + ' to not have the same members as ' + __fmt(list));
  });
  method(['satisfy', 'satisfies'], function (fn) {
    var v = o(this);
    return this._check(!!fn(v), 'expected ' + __fmt(v) + ' to satisfy ' + (fn.name || 'function'), 'expected ' + __fmt(v) + ' to not satisfy ' + (fn.name || 'function'));
  });
  method(['throw', 'throws', 'Throw'], function (want) {
    var fn = o(this), err = null;
    try { fn(); } catch (e) { err = e || new Error(String(e)); }
    var ok = err !== null;
    if (ok && want !== undefined) {
      var m = __errMsg(err);
      if (want instanceof RegExp) ok = want.test(m);
      else if (typeof want === 'string') ok = m.indexOf(want) >= 0;
      else if (typeof want === 'function') ok = err instanceof want;
    }
    return this._check(ok, 'expected function to throw' + (want !== undefined ? ' ' + __fmt(want) : ''), 'expected function not to throw, but it threw ' + (err && __errMsg(err)));
  });
})(Assertion.prototype);

function expect(v, msg) {
  return new Assertion(v, msg);
}
expect.fail = function (msg) {
  throw new AssertionError(msg || 'expect.fail()');
};

function test(name, fn) {
  var rec = { name: String(name), passed: true, error: '' };
  __results.push(rec);
  var fail = function (e) { rec.passed = false; rec.error = __errMsg(e); };
  try {
    var r = fn();
    if (r && typeof r.then === 'function') __pending.push(Promise.resolve(r).catch(fail));
  } catch (e) {
    fail(e);
  }
}

// ---- req ----

function __makeReq(d) {
  d.query = d.query || [];
  d.headers = d.headers || [];
  d.body = d.body || { mode: 'none', text: '', form: [] };
  d.body.form = d.body.form || [];
  var lower = function (s) { return String(s).toLowerCase(); };
  var r = {
    getUrl: function () {
      var q = d.query.filter(function (p) { return p.enabled; }).map(function (p) { return p.key + '=' + p.value; }).join('&');
      if (!q) return d.url;
      return d.url + (d.url.indexOf('?') >= 0 ? '&' : '?') + q;
    },
    setUrl: function (u) {
      u = String(u);
      var i = u.indexOf('?');
      if (i < 0) { d.url = u; d.query = []; return; }
      d.url = u.slice(0, i);
      d.query = u.slice(i + 1).split('&').filter(function (s) { return s !== ''; }).map(function (s) {
        var j = s.indexOf('=');
        return j < 0 ? { key: s, value: '', enabled: true } : { key: s.slice(0, j), value: s.slice(j + 1), enabled: true };
      });
    },
    getMethod: function () { return d.method; },
    setMethod: function (m) { d.method = String(m).toUpperCase(); },
    getName: function () { return d.name; },
    getAuthMode: function () { return (d.auth && d.auth.mode) || 'none'; },
    getHeader: function (name) {
      var n = lower(name), v;
      d.headers.forEach(function (h) { if (h.enabled && lower(h.key) === n) v = h.value; });
      return v;
    },
    getHeaders: function () {
      var out = {};
      d.headers.forEach(function (h) { if (h.enabled) out[h.key] = h.value; });
      return out;
    },
    setHeader: function (name, value) {
      var n = lower(name);
      d.headers = d.headers.filter(function (h) { return lower(h.key) !== n; });
      d.headers.push({ key: String(name), value: String(value), enabled: true });
    },
    setHeaders: function (obj) {
      d.headers = Object.keys(obj || {}).map(function (k) { return { key: k, value: String(obj[k]), enabled: true }; });
    },
    deleteHeader: function (name) {
      var n = lower(name);
      d.headers = d.headers.filter(function (h) { return lower(h.key) !== n; });
    },
    getBody: function (opts) {
      var b = d.body;
      if (opts && opts.raw) return b.text;
      if (b.mode === 'json') { try { return JSON.parse(b.text); } catch (e) { return b.text; } }
      if (b.mode === 'urlencoded') {
        var out = {};
        b.form.forEach(function (p) { if (p.enabled) out[p.key] = p.value; });
        return out;
      }
      if (b.mode === 'none') return undefined;
      return b.text;
    },
    setBody: function (v) {
      var b = d.body;
      if (typeof v === 'string') {
        b.text = v;
        if (b.mode === 'none' || b.mode === 'urlencoded') b.mode = 'text';
        return;
      }
      if (b.mode === 'urlencoded' && v && typeof v === 'object') {
        b.form = Object.keys(v).map(function (k) { return { key: k, value: String(v[k]), enabled: true }; });
        return;
      }
      b.mode = 'json';
      b.text = JSON.stringify(v);
    },
    getTimeout: function () { return 30000; },
    setTimeout: function () { console.warn('req.setTimeout 暂不支持，已忽略'); },
    setMaxRedirects: function () { console.warn('req.setMaxRedirects 暂不支持，已忽略'); }
  };
  Object.defineProperty(r, 'url', { get: r.getUrl, set: r.setUrl, enumerable: true });
  Object.defineProperty(r, 'method', { get: r.getMethod, set: r.setMethod, enumerable: true });
  Object.defineProperty(r, 'headers', { get: r.getHeaders, set: r.setHeaders, enumerable: true });
  Object.defineProperty(r, 'body', { get: r.getBody, set: r.setBody, enumerable: true });
  return r;
}

var req = __makeReq(__reqData);

// ---- res ----

function __getPath(obj, path) {
  if (path === undefined || path === '') return obj;
  var parts = String(path).replace(/\[(\d+)\]/g, '.$1').split('.').filter(function (p) { return p !== ''; });
  var cur = obj;
  for (var i = 0; i < parts.length; i++) {
    if (cur == null) return undefined;
    cur = cur[parts[i]];
  }
  return cur;
}

var res = undefined;
if (typeof __resData !== 'undefined') {
  res = (function (d) {
    var body = d.body;
    try { body = JSON.parse(d.body); } catch (e) { /* keep text */ }
    var r = function (path) { return __getPath(body, path); };
    r.status = d.status;
    r.statusText = d.statusText;
    r.headers = d.headers;
    r.body = body;
    r.responseTime = d.responseTime;
    r.url = d.url;
    r.getStatus = function () { return d.status; };
    r.getStatusText = function () { return d.statusText; };
    r.getHeader = function (name) { return d.headers[String(name).toLowerCase()]; };
    r.getHeaders = function () { return d.headers; };
    r.getBody = function () { return r.body; };
    r.setBody = function (v) { r.body = v; };
    r.getResponseTime = function () { return d.responseTime; };
    r.getUrl = function () { return d.url; };
    r.getSize = function () { return { body: d.size, header: 0, total: d.size }; };
    return r;
  })(__resData);
}

// ---- bru ----

var bru = (function (h) {
  var str = function (v) { return typeof v === 'string' ? v : JSON.stringify(v); };
  var out = {
    getVar: function (k) {
      var r = h.getVar(String(k));
      if (r == null) return undefined;
      return r.json ? JSON.parse(r.value) : r.value;
    },
    setVar: function (k, v) {
      if (!/^[\w.-]+$/.test(String(k))) throw new Error('变量名 "' + k + '" 只能包含字母、数字、-、_ 和 .');
      if (v === undefined) { h.deleteVar(String(k)); return; }
      h.setVar(String(k), str(v), typeof v !== 'string');
    },
    hasVar: function (k) { return h.getVar(String(k)) != null; },
    deleteVar: function (k) { h.deleteVar(String(k)); },
    deleteAllVars: function () { h.deleteAllVars(); },
    getAllVars: function () { return h.allVars(); },
    getEnvVar: function (k) { return h.getEnvVar(String(k)); },
    setEnvVar: function (k, v, opts) { h.setEnvVar(String(k), str(v), !!(opts && opts.persist)); },
    hasEnvVar: function (k) { return h.getEnvVar(String(k)) !== undefined; },
    deleteEnvVar: function (k) { h.deleteEnvVar(String(k)); },
    getEnvName: function () { return h.envName(); },
    getCollectionVar: function (k) { return h.scopeVar('collection', String(k)); },
    getFolderVar: function (k) { return h.scopeVar('folder', String(k)); },
    getRequestVar: function (k) { return h.scopeVar('request', String(k)); },
    getGlobalEnvVar: function () { return undefined; },
    getCollectionName: function () { return h.collectionName(); },
    getProcessEnv: function (k) { return h.processEnv(String(k)); },
    interpolate: function (s) {
      if (typeof s !== 'string') return JSON.parse(h.interpolate(JSON.stringify(s)));
      return h.interpolate(s);
    },
    sleep: function (ms) { h.sleep(Number(ms) || 0); return Promise.resolve(); },
    setNextRequest: function () { console.warn('bru.setNextRequest 只在集合运行器中生效，已忽略'); },
    runner: {
      setNextRequest: function () { console.warn('bru.runner.setNextRequest 只在集合运行器中生效，已忽略'); },
      skipRequest: function () { console.warn('bru.runner.skipRequest 只在集合运行器中生效，已忽略'); },
      stopExecution: function () { console.warn('bru.runner.stopExecution 只在集合运行器中生效，已忽略'); }
    },
    runRequest: function () { return Promise.reject(new Error('bru.runRequest 暂不支持')); },
    sendRequest: function () { return Promise.reject(new Error('bru.sendRequest 暂不支持')); }
  };
  return out;
})(__host);

// ---- vars:post-response and assert blocks ----

function __evalExpr(expr) {
  return (new Function('res', 'req', 'bru', '"use strict"; return (' + expr + ');'))(res, req, bru);
}

function __setPostVar(key, expr) {
  bru.setVar(key, __evalExpr(expr));
}

function __parseOperand(s) {
  s = s.trim();
  if (/^-?\d+(\.\d+)?([eE][-+]?\d+)?$/.test(s)) return Number(s);
  if (s === 'true') return true;
  if (s === 'false') return false;
  if (s === 'null') return null;
  if (s === 'undefined') return undefined;
  if (s.length >= 2 && ((s[0] === '"' && s[s.length - 1] === '"') || (s[0] === "'" && s[s.length - 1] === "'"))) return s.slice(1, -1);
  if (s[0] === '[' || s[0] === '{') { try { return JSON.parse(s); } catch (e) { /* plain string */ } }
  return s;
}

var __assertOps = {
  eq: function (a, b) { return __deepEq(a, b); },
  neq: function (a, b) { return !__deepEq(a, b); },
  gt: function (a, b) { return a > b; },
  gte: function (a, b) { return a >= b; },
  lt: function (a, b) { return a < b; },
  lte: function (a, b) { return a <= b; },
  in: function (a, b) { return b.some(function (x) { return __deepEq(a, x); }); },
  notIn: function (a, b) { return !b.some(function (x) { return __deepEq(a, x); }); },
  contains: function (a, b) { return a != null && (typeof a === 'string' ? a.indexOf(String(b)) >= 0 : Array.isArray(a) && a.some(function (x) { return __deepEq(x, b); })); },
  notContains: function (a, b) { return !__assertOps.contains(a, b); },
  length: function (a, b) { return a != null && a.length === b; },
  matches: function (a, b) { return new RegExp(b).test(String(a)); },
  notMatches: function (a, b) { return !new RegExp(b).test(String(a)); },
  startsWith: function (a, b) { return typeof a === 'string' && a.indexOf(String(b)) === 0; },
  endsWith: function (a, b) { return typeof a === 'string' && a.slice(-String(b).length) === String(b); },
  between: function (a, b) { return a >= b[0] && a <= b[1]; },
  isEmpty: function (a) { return a == null || a === '' || (Array.isArray(a) && a.length === 0) || (typeof a === 'object' && Object.keys(a).length === 0); },
  isNotEmpty: function (a) { return !__assertOps.isEmpty(a); },
  isNull: function (a) { return a === null; },
  isUndefined: function (a) { return a === undefined; },
  isDefined: function (a) { return a !== undefined; },
  isTruthy: function (a) { return !!a; },
  isFalsy: function (a) { return !a; },
  isJson: function (a) { return a !== null && typeof a === 'object'; },
  isNumber: function (a) { return typeof a === 'number'; },
  isString: function (a) { return typeof a === 'string'; },
  isBoolean: function (a) { return typeof a === 'boolean'; },
  isArray: function (a) { return Array.isArray(a); }
};
var __listOps = { in: true, notIn: true, between: true };

function __runAssert(lhs, rhs) {
  var rec = { name: lhs + ': ' + rhs, passed: true, error: '' };
  __asserts.push(rec);
  try {
    var m = /^\s*(\w+)\b\s*([\s\S]*)$/.exec(rhs);
    var op = 'eq', operand = rhs;
    if (m && Object.prototype.hasOwnProperty.call(__assertOps, m[1])) { op = m[1]; operand = m[2]; }
    var actual = __evalExpr(lhs);
    var want;
    if (__listOps[op]) {
      var t = operand.trim();
      want = t[0] === '[' ? JSON.parse(t) : t.split(',').map(__parseOperand);
    } else if (op === 'matches' || op === 'notMatches') {
      want = operand.trim().replace(/^\/(.*)\/$/, '$1');
    } else {
      want = __parseOperand(operand);
    }
    if (!__assertOps[op](actual, want)) {
      rec.passed = false;
      rec.error = 'expected ' + __fmt(actual) + ' ' + op + (operand.trim() ? ' ' + operand.trim() : '');
    }
  } catch (e) {
    rec.passed = false;
    rec.error = __errMsg(e);
  }
}
