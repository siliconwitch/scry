import { i as e } from "./document-content-DXDaB_ij.js";
import { n as t } from "./cjk-fallback-CxqXJu32.js";
import { i as n, r } from "./error-wire-Dr1HjXGZ.js";
import { C as i, T as a, a as o, i as s, l as c, r as l } from "./document-pull-client-DsaDuAP-.js";
import { a as u, r as d } from "./wasm-loader-aCVZKhJM.js";
import { r as f } from "./document-pull-worker-l4KpxZ8T.js";
import { i as p, t as m } from "./docx-B5Kih6eq.js";
import { t as h } from "./model-source-Dlc0apkZ.js";
var g = class extends m {
	constructor(...e) {
		super(...e);
	}
	renderPage(e, t = {}) {
		return super.renderPage(e, {
			...t,
			showTrackedChanges: !0
		});
	}
};
async function _(e, n, s) {
	let u = t(n.cjkFallback), { acquired: f, viewDefaults: p } = await y(e, n, s, l);
	try {
		if (n.signal?.aborted) {
			let e = /* @__PURE__ */ Error("DOCX document session was aborted");
			throw e.name = "AbortError", e;
		}
		let e = n.factory.createCanvas(1, 1), t = c(f.result, {
			cjkFallback: u,
			measureContext: e.getContext("2d"),
			createCanvas: (e, t) => n.factory.createCanvas(e, t),
			allowFootnoteContinuation: n.allowFootnoteContinuation !== !1
		}), r = d(n.currentDate), s = i(f.result, n.signal);
		s && await s;
		let l = o(f.result, t, r), h = p.showTrackedChanges === !0, _ = h ? l.layoutVariants.layoutFor(a(r, r, !0)) : l.layoutVariants.defaultLayout, v = new (h ? g : m)(f.closeArchive, f.archive, f.result, t, _, n.factory, r, f.usage, f.metrics, n.signal);
		return f.metrics.observeUsage(v.resourceUsage), f.metrics.checkpoint("pagination ready"), v;
	} catch (e) {
		try {
			f.closeArchive();
		} catch {}
		let t = r(e) ?? e;
		throw f.metrics.fail(t), t;
	}
}
async function v(t, n, r) {
	return u(async () => {
		let { acquired: e } = await y(t, n, r, s), i = !1;
		return {
			acquired: e,
			markSucceeded: () => {
				i = !0;
			},
			close: async () => {
				try {
					e.closeArchive(), i && e.metrics.succeed({ documents: 1 });
				} catch (t) {
					throw e.metrics.fail(t), t;
				}
			}
		};
	}, async ({ acquired: t, markSucceeded: n }) => {
		try {
			let r = e(t.result);
			return t.metrics.checkpoint("document materialized", t.usage), n(), r;
		} catch (e) {
			throw t.metrics.fail(e), e;
		}
	});
}
async function y(e, t, r, i) {
	let { acquireDocxSessionFromArchive: a, validateDocxModelSourceArchive: o, validateDocxModelSourceViewDefaults: s } = await import("./model-source-session-DFPAEdJS.js"), c = await h(e, "docx", {
		...t,
		sources: [t.legacyDoc, t.odt]
	}, o, (e) => n(e) ?? f(e));
	if (c.kind === "ooxml") return {
		acquired: await p(c.bytes, r(), t, i),
		viewDefaults: {}
	};
	let l;
	try {
		l = s(c.opened.viewDefaults);
	} catch (e) {
		try {
			c.opened.close();
		} catch {}
		throw e;
	}
	return {
		acquired: await a({
			archive: c.opened.archive,
			sourceByteLength: c.sourceByteLength,
			closeArchive: c.opened.close
		}, t, i),
		viewDefaults: l
	};
}
export { v as materializeDocxSource, _ as openDocxSource };
