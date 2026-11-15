// standalone check of the domain-dot aggregation logic (extracted 1:1 from index.html)
const viewData = new Map();
const inflight = new Set();
let domains_off = [];
const s = { domains_off };

function domainDot(d) {
  const kids = [];
  viewData.forEach((v) => { if (v.kind === "app" && v.domain === d) kids.push(v); });
  for (const k of inflight) {
    const v = viewData.get(k);
    if (v && v.kind === "app" && v.domain === d) return { t: "🟠", title: "starting/stopping…" };
  }
  const isOff = (s.domains_off || []).includes(d);
  const serving = kids.filter(k => k.served).length;
  if (isOff) return { t: "🔴", title: "domain off — click to enable all its apps" };
  if (kids.length && serving === kids.length) return { t: "🟢", title: "all apps serving — click to switch the domain off" };
  if (serving === 0) return { t: "🔴", title: "all stopped — click to enable the domain" };
  return { t: "🔵", title: "partially running (" + serving + "/" + kids.length + ")" };
}

function app(vmid, domain, served, disabled) {
  viewData.set("v" + vmid, { vmid, domain, kind: "app", served, disabled, stale: false });
}

// 3 apps on example.com: all serving
app(1, "example.com", true, false);
app(2, "example.com", true, false);
app(3, "example.com", true, false);
// 1 app on example.org: stopped
app(4, "example.org", false, true);
// mixed domain: 1 serving + 1 stopped
app(5, "mixed.example", true, false);
app(6, "mixed.example", false, false);

const cases = {
  allGreen: domainDot("example.com").t,   // wait — example.com has 3 serving -> green
  allStopped: domainDot("example.org").t,
  partial: domainDot("mixed.example").t,
};
console.log(JSON.stringify(cases));

// transitions: in-flight child -> orange
inflight.add("v2");
const inflightDot = domainDot("example.com").t;
inflight.delete("v2");

// global domain off -> red regardless of children
s.domains_off = ["example.com"];
const offDot = domainDot("example.com").t;
domains_off = [];

const ok = cases.allGreen === "🟢" && cases.allStopped === "🔴" && cases.partial === "🔵"
  && inflightDot === "🟠" && offDot === "🔴";
console.log(JSON.stringify({ inflightDot, offDot }));
console.log(ok ? "AGG-OK: 🟢/🔴/🔵/🟠/off-red all correct" : "AGG-FAIL");
process.exit(ok ? 0 : 1);
