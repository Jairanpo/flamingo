const state = { q:"", source:"", collection:"", have:null, starred:false,
                unwatched:false, hidden:false, videos:[], ordered:false, sources:[],
                collections:[], sort:"default", toggl:{}, togglChannels:[],
                all:false, pins:[], maxPins:5 };
const $ = s => document.querySelector(s);
// Downloads live on a computer, so they only show when the page came
// through that computer's agent (phase 6); otherwise everything streams.
const ON_DEVICE = document.body.dataset.device !== "";
const fmtDur = s => { if(!s) return ""; s=Math.round(s);
  const h=Math.floor(s/3600), m=Math.floor(s%3600/60), x=s%60;
  return h ? `${h}:${String(m).padStart(2,"0")}:${String(x).padStart(2,"0")}`
           : `${m}:${String(x).padStart(2,"0")}`; };
const fmtDate = v => !v.date ? "" : (v.date_approx ? "~" + v.date.slice(0,7) : v.date);
const fmtSize = b => b ? (b/1e9 >= 1 ? (b/1e9).toFixed(1)+" GB" : Math.round(b/1e6)+" MB") : "";

async function api(path, opts) {
  const r = await fetch(path, opts);
  if (r.status === 401) {
    // Signed out elsewhere, or the session ran out: back to the door.
    location.href = "/login?next=" + encodeURIComponent(location.pathname + location.search);
    throw new Error("sign in first");
  }
  if (!r.ok) throw new Error((await r.json().catch(()=>({}))).error || r.statusText);
  return r.json();
}
const post = (p, body) => api(p, {method:"POST", headers:{"Content-Type":"application/json"},
                                 body: JSON.stringify(body||{})});

function params() {
  const p = new URLSearchParams();
  if (state.q) p.set("q", state.q);
  if (state.source) p.set("source", state.source);
  if (state.collection) p.set("collection", state.collection);
  if (state.have !== null) p.set("have", state.have ? "1" : "0");
  if (state.starred) p.set("starred", "1");
  if (state.unwatched) p.set("unwatched", "1");
  if (state.hidden) p.set("hidden", "1");
  if (state.all) p.set("all", "1");
  return p.toString();
}

let pickerKey = "", dashKey = "";
// The sidebar sits right under the header, whose height changes as it wraps.
new ResizeObserver(([e]) => document.documentElement.style
  .setProperty("--hh", `${e.target.offsetHeight}px`)).observe($("header"));
async function refresh(opts) {
  const qs = params();
  // The view lives in the address bar, so a refresh (or a bookmark) lands you
  // where you were rather than back on Everything. Picking a source is a
  // navigation and gets its own history entry; typing in the search box or
  // flipping a chip only rewrites the current one.
  const step = opts && opts.push ? "pushState" : "replaceState";
  history[step](null, "", qs ? "?" + qs : location.pathname);
  const d = await api("/api/state?" + qs);
  state.videos = d.videos;
  state.ordered = d.ordered;
  state.sources = d.sources;
  state.collections = d.collections;
  state.sort = d.sort;
  state.toggl = d.toggl || {};
  state.togglChannels = d.toggl_channels || [];
  state.pins = d.pins || [];
  state.maxPins = d.max_pins || 5;
  buildPicker(d.sources, d.collections);
  buildSorts(d.sorts, d.sort);
  const s = d.stats;
  // Disk use only means something where downloads happen; empty parts drop
  // out so no stray separators are left behind.
  $("#stats").textContent = [
    ON_DEVICE && `${s.have}/${s.total} on disk`,
    ON_DEVICE && fmtSize(s.bytes),
    `${s.channels} channels`, `${s.playlists} playlists`,
    s.hidden && `${s.hidden} hidden`,
  ].filter(Boolean).join(" · ");
  render(d.videos);
  paintDash();
  paintCont();
  paintViewbar();
  paintViewProgress();
  renderJobs(d.jobs);
}

// One mark per kind of source, the same ones `yt sources` prints.
const GLYPHS = {channel:"▸", course:"◆", live:"●", playlist:"▤", picks:"✦"};
const glyph = s => GLYPHS[s.kind === "channel" ? "channel" : s.section || "playlist"];
// Focus's mark, a target; and the play triangle the Focus buttons share.
const TARGET = `<svg class="ico" viewBox="0 0 24 24" width="16" height="16" fill="none"
  stroke="currentColor" stroke-width="2" aria-hidden="true"><circle cx="12" cy="12" r="9"/>
  <circle cx="12" cy="12" r="3.5"/></svg>`;
const PLAY = `<svg viewBox="0 0 24 24" width="1em" height="1em" aria-hidden="true">
  <path d="M7 4l13 8-13 8z" fill="currentColor"/></svg>`;
const fmtDay = t => new Date(t * 1000).toLocaleDateString(undefined, {month: "short", day: "numeric"});

/* ---- sidebar: topics, then creators, then their channel and playlists ----
   A dropdown stops scaling past a dozen creators. Here every creator sits on
   a topic shelf you choose (Japanese, Unreal...), folds open to what you track
   from them, and the box at the top narrows the whole tree as you type. */
let sideOpen = new Set();
try { sideOpen = new Set(JSON.parse(localStorage.getItem("flamingo.side-open") || "[]")); }
catch (e) {}
const saveSideOpen = () => {
  try { localStorage.setItem("flamingo.side-open", JSON.stringify([...sideOpen])); } catch (e) {}
};
let sideFilter = "";
const NO_TOPIC = "No topic";

function buildPicker(sources, colls) {
  const known = state.source ? sources.some(s => s.id === state.source)
              : state.collection ? colls.some(c => String(c.id) === String(state.collection))
              : true;
  if (!known) {
    // A URL naming a source that has since been forgotten: back to Everything.
    state.source = state.collection = "";
    refresh();
    return;
  }
  paintWho(sources, colls);
  paintShelve(sources);
  // The 1.2s job polling refreshes too; rebuilding under an open topic box
  // would throw away what you were typing.
  if ($("#side .tedit")) return;
  const key = JSON.stringify([sources.map(s => [s.id, s.group, s.topic, s.section,
                                                 s.creator, s.shelved, s.pinned, s.completed,
                                                 s.seen, s.total]),
                              colls.map(c => [c.id, c.seen, c.total]),
                              sideFilter, [...sideOpen], state.source, state.collection,
                              state.all, state.pins.map(p => p.id)]);
  if (key === pickerKey) return;
  pickerKey = key;
  paintSide(sources, colls);
}

function paintSide(sources, colls) {
  const tree = $("#sidetree");
  tree.replaceChildren();
  let out = tree;                       // the section rows go into
  const q = sideFilter.toLowerCase();
  const hit = (...xs) => !q || xs.some(x => x && x.toLowerCase().includes(q));
  const el = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  };
  const count = it => it.total ? `${it.seen}/${it.total}` : "";
  const row = (label, id, it, depth) => {
    const b = el("button", "sitem" + (id === activeId() ? " on" : ""));
    b.style.paddingLeft = `${8 + depth * 14}px`;
    b.append(el("span", "nm", label), el("span", "ct", count(it)));
    b.title = it.total ? `${label} — ${it.seen} watched, ${it.have} on disk, of ${it.total}` : label;
    b.onclick = () => pick(id);
    return b;
  };
  // A fold: its header toggles it open; searching opens everything it holds.
  const fold = (k, label, n, depth, extra) => {
    const open = !!q || sideOpen.has(k);
    const h = el("div", "sfold");
    h.style.paddingLeft = `${4 + depth * 14}px`;
    const t = el("button", "tw", open ? "▾" : "▸");
    const nm = el("button", "nm", label);
    const toggle = () => {
      sideOpen.has(k) ? sideOpen.delete(k) : sideOpen.add(k);
      saveSideOpen(); pickerKey = ""; buildPicker(state.sources, state.collections);
    };
    t.onclick = toggle;
    nm.onclick = extra && extra.onName ? extra.onName : toggle;
    h.append(t, nm);
    if (n != null) h.append(el("span", "ct", n));
    if (extra && extra.tail) h.append(extra.tail);
    if (extra && extra.cls) h.classList.add(extra.cls);
    out.append(h);
    return open;
  };

  // Focus: what you are working through, first and never folded, in the
  // order you added it. Its heading is the way home.
  const rank = new Map(state.pins.map((p, i) => [p.id, i]));
  const focus = sources.filter(s => s.pinned)
    .sort((a, b) => (rank.get(a.id) ?? 99) - (rank.get(b.id) ?? 99));
  if (focus.length) {
    const box = el("section", "sfocus");
    const h = el("button", "sfhead" + (activeId() === "" ? " on" : ""));
    h.innerHTML = `${TARGET}<span class="nm">Focus</span>
      <span class="ct">${focus.length} of ${state.maxPins}</span>`;
    h.title = "Your Focus dashboard";
    h.onclick = () => pick("");
    box.append(h);
    for (const s of focus) {
      const done = s.total > 0 && s.seen >= s.total;
      const pct = s.total ? Math.round(100 * s.seen / s.total) : 0;
      const b = el("button", "sfitem" + (activeId() === "s:" + s.id ? " on" : "") + (done ? " done" : ""));
      const top = el("span", "fl");
      top.append(el("span", "nm", s.kind === "channel" ? `${s.name} — all videos` : s.name),
                 el("span", "ct", done ? "Done ✓" : `${pct}%`));
      const bar = el("span", "fbar"), fill = el("i");
      fill.style.width = `${done ? 100 : pct}%`;
      bar.append(fill);
      b.append(top, bar);
      b.title = `${s.name} — ${s.seen} of ${s.total} watched`;
      b.onclick = () => pick("s:" + s.id);
      box.append(b);
    }
    tree.append(box);
  }

  // Backlog: everything tracked that is waiting its turn.
  const waiting = sources.filter(s => !s.shelved && !s.pinned && !s.completed);
  const back = el("section", "sback");
  const bh = el("div", "shead");
  bh.append(el("span", "nm", "Backlog"), el("span", "ct", `${waiting.length} waiting`));
  back.append(bh);
  tree.append(back);
  out = back;
  // With a Focus, home is its dashboard and Everything its own view.
  out.append(row("Everything", state.pins.length ? "all" : "", {}, 0));

  // Group waiting sources by creator (the server sends them already
  // ordered), then creators by topic.
  const creators = new Map();
  for (const s of waiting) {
    if (!creators.has(s.group))
      creators.set(s.group, {key: s.group, name: s.creator || "unknown creator",
                             handle: s.handle, topic: s.topic || NO_TOPIC, items: []});
    creators.get(s.group).items.push(s);
  }
  const topics = new Map();
  for (const c of creators.values()) {
    if (!topics.has(c.topic)) topics.set(c.topic, []);
    topics.get(c.topic).push(c);
  }
  const names = [...topics.keys()].sort((a, b) =>
    (a === NO_TOPIC) - (b === NO_TOPIC) || a.localeCompare(b));
  // Everyone on one shelf needs no shelf at all.
  const flat = names.length === 1 && names[0] === NO_TOPIC;
  for (const t of names) {
    const all = topics.get(t);
    const topicHit = q && t !== NO_TOPIC && hit(t);
    const shown = all.map(c => {
      const whole = topicHit || hit(c.name, c.handle);
      const items = whole ? c.items : c.items.filter(s => hit(s.name));
      return items.length ? {...c, items} : null;
    }).filter(Boolean);
    if (!shown.length) continue;
    const depth = flat ? 0 : 1;
    if (!flat && !fold("t:" + t, t, all.length, 0)) continue;
    for (const c of shown) {
      const chan = c.items.find(s => s.kind === "channel");
      const edit = el("button", "tbtn", "⋯");
      edit.title = `Topic for ${c.name}`;
      edit.onclick = e => { e.stopPropagation(); editTopic(edit, c); };
      // With their channel tracked, the name opens all their videos.
      const open = fold("g:" + c.key, c.name, null, depth, {
        tail: edit,
        onName: chan ? () => pick("s:" + chan.id) : null});
      if (!open) continue;
      for (const s of c.items)
        out.append(row(s.kind === "channel" ? `${glyph(s)} All videos` : `${glyph(s)} ${s.name}`,
                       "s:" + s.id, s, depth + 1));
    }
  }
  out = tree;

  // Completed: finished, newest first, with the day you finished.
  const finished = sources.filter(s => s.completed && hit(s.name, s.creator))
    .sort((a, b) => b.completed - a.completed);
  if (finished.length && fold("completed", "✓ Completed", finished.length, 0, {cls: "green"}))
    for (const s of finished) {
      const r = row(s.kind === "channel" ? `${glyph(s)} ${s.name}` : `${glyph(s)} ${s.name}`,
                    "s:" + s.id, s, 1);
      r.querySelector(".ct").textContent = fmtDay(s.completed);
      r.title = `${s.name} — finished ${new Date(s.completed * 1000).toLocaleDateString()}`;
      tree.append(r);
    }

  const cs = colls.filter(c => hit(c.name));
  if (cs.length && fold("collections", "Collections", colls.length, 0))
    for (const c of cs) tree.append(row(c.name, "c:" + c.id, c, 1));
  const shelved = sources.filter(s => s.shelved && hit(s.name, s.creator));
  if (shelved.length && fold("shelved", "Shelved", shelved.length, 0))
    for (const s of shelved)
      tree.append(row(s.kind === "channel" ? `${glyph(s)} ${s.name}`
                                           : `${glyph(s)} ${s.name} — ${s.creator || "?"}`,
                      "s:" + s.id, s, 1));
  if (q && tree.querySelectorAll(".sitem").length <= 1)
    tree.append(el("div", "snone", "Nothing tracked matches."));
}

const activeId = () => state.source ? "s:" + state.source
                     : state.collection ? "c:" + state.collection
                     : state.all && state.pins.length ? "all" : "";

function pick(id) {
  state.source     = id.startsWith("s:") ? id.slice(2) : "";
  state.collection = id.startsWith("c:") ? id.slice(2) : "";
  state.all        = id === "all";
  document.body.classList.remove("side-on");          // phone: get out of the way
  refresh({push: true});
}

// An inline box, suggesting the topics already in use; Enter keeps it,
// Escape or leaving it blank-and-unchanged walks away.
function editTopic(btn, c) {
  const inp = document.createElement("input");
  inp.className = "tedit";
  inp.value = c.topic === NO_TOPIC ? "" : c.topic;
  inp.placeholder = "Topic, e.g. Japanese";
  inp.setAttribute("list", "topiclist");
  $("#topiclist").replaceChildren(...[...new Set(state.sources.map(s => s.topic).filter(Boolean))]
    .sort().map(t => Object.assign(document.createElement("option"), {value: t})));
  btn.replaceWith(inp);
  inp.focus(); inp.select();
  let done = false;
  const finish = async save => {
    if (done) return;
    done = true;
    if (save) {
      const topic = inp.value.trim();
      await post("/api/topic", {creator: c.key, topic}).catch(e => alert(e.message));
      if (topic) sideOpen.add("t:" + topic);
      saveSideOpen();
    }
    inp.remove();
    pickerKey = "";
    refresh();
  };
  inp.onkeydown = e => {
    if (e.key === "Enter") finish(true);
    else if (e.key === "Escape") { e.stopPropagation(); finish(false); }
  };
  inp.onblur = () => finish(true);
}

$("#sidefilter").addEventListener("input", e => {
  sideFilter = e.target.value.trim();
  pickerKey = ""; buildPicker(state.sources, state.collections);
});
$("#sidebtn").onclick = () => document.body.classList.toggle("side-on");

// The creator of the view you are in, kept beside the picker: a collapsed
// select shows the option text alone, and the option text is the bit that is
// ambiguous between two playlists called the same thing.
function paintWho(sources, colls) {
  const el = $("#whosrc");
  const src = state.source && sources.find(s => s.id === state.source);
  let text = "";
  if (src && src.kind === "playlist")
    text = glyph(src) + " " + (src.creator || "unknown creator");
  else if (src)
    text = glyph(src) + " " + (src.creator || src.name);
  else if (state.collection)
    text = "yours";
  const handle = src && src.handle;
  if (handle) text += ` · ${handle} — more →`;
  el.textContent = text;
  el.classList.toggle("more", !!handle);
  el.dataset.handle = handle || "";
  el.title = handle ? `See ${handle}'s courses, live and playlists, and track more`
                    : text && src ? src.name : "";
  el.hidden = !text;
}
$("#whosrc").onclick = () => {
  const handle = $("#whosrc").dataset.handle;
  if (!handle) return;
  $("#newsrc").onclick();
  $("#snew").value = handle;
  browseSource();
};

// Only a source can be shelved; the button names what pressing it would do.
function paintShelve(sources) {
  const b = $("#shelve");
  const src = state.source && sources.find(s => s.id === state.source);
  b.hidden = $("#forget").hidden = $("#pin").hidden = $("#complete").hidden = !src;
  if (!src) return;
  const pb = $("#pin"), full = !src.pinned && state.pins.length >= state.maxPins;
  pb.textContent = src.pinned ? "Remove from Focus"
                 : src.completed ? "◎ Back to Focus" : "◎ Move to Focus";
  pb.classList.toggle("quiet", src.pinned);
  pb.disabled = full;
  pb.title = src.pinned ? "Take it off your Focus; it goes back to the backlog"
           : full ? `${state.maxPins} in Focus already — finish one first`
           : "Put it on your Focus dashboard, the things you are working through now";
  const cb = $("#complete");
  cb.textContent = src.completed ? `✓ Completed ${fmtDay(src.completed)}` : "✓ Mark complete";
  cb.classList.toggle("quiet", !src.pinned && !src.completed);
  cb.title = src.completed ? "Finished. Press to undo and send it back to the backlog"
           : "Finished with it: off your Focus and into Completed, with today's date";
  $("#forget").title = "Stop tracking it. Downloaded files, stars and " +
                       "collections stay; add it again any time.";
  b.textContent = src.shelved ? "Unshelve" : "Shelve";
  b.title = src.shelved
    ? "Bring it back into the picker, Everything and full syncs"
    : "Put it away for later: out of the picker, Everything and full syncs. " +
      "Still tracked, and still under Shelved in the picker.";
}
$("#shelve").onclick = async () => {
  const src = state.sources.find(s => s.id === state.source);
  if (!src) return;
  await post("/api/shelve", {source: src.id, value: !src.shelved});
  refresh();
};
$("#pin").onclick = async () => {
  const src = state.sources.find(s => s.id === state.source);
  if (!src) return;
  try { await post("/api/pin", {source: src.id, value: !src.pinned}); }
  catch (e) { alert(e.message); }
  refresh();
};
$("#complete").onclick = async () => {
  const src = state.sources.find(s => s.id === state.source);
  if (!src) return;
  try { await post("/api/complete", {source: src.id, value: !src.completed}); }
  catch (e) { alert(e.message); }
  refresh();
};
$("#forget").onclick = async () => {
  const src = state.sources.find(s => s.id === state.source);
  if (!src) return;
  const name = src.kind === "channel" ? `${src.name || src.url} (all videos)`
                                      : src.name || src.url;
  if (!confirm(`Stop tracking ${name}?\n\nDownloaded files, stars and ` +
               `collections stay.`)) return;
  await post("/api/forget", {source: src.id});
  pick("");
};

function buildSorts(sorts, current) {
  const sel = $("#sort");
  if (!sel.options.length) {
    for (const s of sorts) {
      const o = document.createElement("option");
      o.value = s.key; o.textContent = s.label;
      sel.appendChild(o);
    }
  }
  // The server decides which sort this view gets, so the dropdown follows it
  // rather than the other way round -- switching source shows that view's own.
  // Only when it actually differs: job polling refreshes every 1.2s and must
  // not reach into a dropdown the user has open.
  if (sel.value !== current) sel.value = current;
}

// How long a card is given to play itself out before the page is rebuilt
// around its absence.
const SINK_MS = 240;
const REDUCED = matchMedia("(prefers-reduced-motion: reduce)").matches;
let foldOpen = false;
try { foldOpen = localStorage.getItem("flamingo.watched-open") === "1"; } catch (e) {}

// A card in mid-flight between the grid and the fold must not be torn out from
// under its own animation, and job polling rebuilds the page every 1.2s.
let sinking = 0;
function moveOut(el, cls) {
  sinking++;
  el.classList.add(cls);
  setTimeout(() => { if (!--sinking) render(state.videos); },
             REDUCED ? 0 : SINK_MS);
}

function render(vs) {
  if (sinking) return;
  const grid = $("#grid"), empty = $("#empty"), fold = $("#fold");
  grid.innerHTML = "";
  fold.hidden = true;
  document.body.classList.remove("hasfold");
  empty.classList.remove("thin");
  empty.hidden = true;
  if (isHome()) return;
  empty.hidden = vs.length > 0;
  if (!vs.length) {
    empty.textContent = state.hidden
      ? "Nothing is hidden."
      : state.q || state.source || state.collection || state.have !== null
      ? "Nothing matches those filters."
      : "Catalog is empty — press ＋ source above, or run  yt add @channel.";
    return;
  }
  // Under a playlist's or a collection's own order the position numbers are
  // the whole point, so nothing is allowed to move; everywhere else what you
  // have already seen drops below the fold.
  const split = !state.ordered;
  const live = split ? vs.filter(v => !v.watched) : vs;
  const seen = split ? vs.filter(v =>  v.watched) : [];
  live.forEach((v, i) => grid.appendChild(card(v, state.ordered ? i + 1 : null)));

  if (!live.length && seen.length) {
    empty.hidden = false;
    empty.classList.add("thin");
    empty.textContent = "You have watched everything here.";
  }
  if (!seen.length) return;
  fold.hidden = false;
  document.body.classList.add("hasfold");
  $("#foldn").textContent = `Watched · ${seen.length}`;
  const list = $("#seenlist");
  list.innerHTML = "";
  seen.forEach(v => list.appendChild(seenRow(v)));
  paintFold();
}

/* ---- home: your Focus ----
   A short list of what you are working through, in place of the whole
   library. One is ideal; each one more is a little less focus. The words
   guide, they don't scold. */
const LEVELS = [
  null,
  "One thing on your desk — the ideal amount.",
  "Two things on your desk — really good.",
  "Three things on your desk — a good number. Keep it here.",
  "Four things on your desk — finishing one will help the others.",
  "Five things on your desk — that's the limit. Finish one before adding more.",
];
const isHome = () => !state.source && !state.collection && !state.q && !state.all
  && state.have === null && !state.starred && !state.unwatched && !state.hidden
  && state.pins.length > 0;

function paintDash() {
  const box = $("#dash");
  box.hidden = !isHome();
  if (box.hidden) { box.replaceChildren(); dashKey = ""; return; }
  // Unchanged since last time: leave it be rather than redraw every card.
  const key = JSON.stringify([state.pins, state.pins.map(p =>
    state.sources.find(x => x.id === p.id))]);
  if (key === dashKey) return;
  dashKey = key;
  const n = state.pins.length, max = state.maxPins;
  const items = state.pins.map(p => ({p, s: state.sources.find(x => x.id === p.id)}))
                          .filter(x => x.s);
  // The oldest Focus with something left to watch is the one to continue.
  const hero = items.find(x => x.p.next);
  let bars = "";
  for (let i = 0; i < max; i++) bars += `<i class="${i < n ? (n >= 4 ? "on warm" : "on") : ""}"></i>`;
  box.innerHTML = `
    <div class="depth" aria-hidden="true">
      <div class="dl far"></div><div class="dl mid"></div><div class="dl near"></div>
    </div>
    <div class="fhead">
      <div class="ftitle"><h1>Your Focus</h1><p class="say"></p></div>
      <div class="fmeter" role="img" aria-label="${n} of ${max} Focus places used">${bars}</div>
      <button class="link" id="dall">All videos →</button>
    </div>
    <div class="fbody"></div>`;
  box.querySelector(".say").textContent = LEVELS[Math.min(n, LEVELS.length - 1)];
  $("#dall").onclick = () => pick("all");
  const body = box.querySelector(".fbody");
  if (hero) body.append(heroCard(hero.s, hero.p));
  const rest = items.filter(x => x !== hero);
  if (rest.length) {
    const g = document.createElement("div");
    g.className = "fcards";
    for (const x of rest) g.append(focusCard(x.s, x.p));
    body.append(g);
  }
}

const dropFocus = async s => { await post("/api/pin", {source: s.id, value: false}); refresh(); };
const markDone = async s => { await post("/api/complete", {source: s.id, value: true}); refresh(); };
const srcLabel = s => s.kind === "channel" ? `${s.name} — all videos` : s.name;

// The one to continue: big, warm, and first.
function heroCard(s, p) {
  const v = p.next;
  const el = document.createElement("section");
  el.className = "fhero";
  const left = Math.max(0, s.total - s.seen);
  const pct = s.total ? Math.round(100 * s.seen / s.total) : 0;
  el.innerHTML = `
    <button class="fthumb" title="Play the next one">
      <img loading="lazy" src="/thumb/${v.id}" alt="">
      <span class="play">${PLAY}</span>
      ${v.duration ? `<span class="dur">${fmtDur(v.duration)}</span>` : ""}
    </button>
    <div class="finfo">
      <span class="k">Continue here</span>
      <h2 class="t"></h2>
      <button class="src" title="Open it"></button>
      <div class="fprog"><span class="track"><i style="width:${pct}%"></i></span>
        <span class="n">${s.seen} of ${s.total} watched${left ? ` · ${left} to go` : ""}</span></div>
      <div class="facts">
        <button class="fplay">${PLAY} Play next</button>
        <button class="fopen">Open</button>
        <button class="link fdrop" title="Take it off your Focus; it goes back to the backlog">Remove from Focus</button>
      </div>
    </div>`;
  el.querySelector(".t").textContent = v.title;
  el.querySelector(".src").textContent = `${glyph(s)} ${srcLabel(s)}`;
  el.querySelector(".fthumb").onclick = el.querySelector(".fplay").onclick = () => open_(v, s.id);
  el.querySelector(".src").onclick = el.querySelector(".fopen").onclick = () => pick("s:" + s.id);
  el.querySelector(".fdrop").onclick = () => dropFocus(s);
  if (p.after && p.after.length) {
    const q = document.createElement("div");
    q.className = "fthen";
    q.innerHTML = `<span class="k2">Then</span>`;
    for (const a of p.after) {
      const r = document.createElement("button");
      r.className = "fq";
      r.innerHTML = `<span class="t"></span><span class="d">${a.duration ? fmtDur(a.duration) : ""}</span>`;
      r.querySelector(".t").textContent = a.title;
      r.onclick = () => open_(a, s.id);
      q.append(r);
    }
    el.querySelector(".finfo").append(q);
  }
  return el;
}

// The others: smaller and cooler. One you have finished turns green and
// offers to put itself away in Completed.
function focusCard(s, p) {
  const el = document.createElement("article");
  const v = p.next;
  const pct = s.total ? Math.round(100 * s.seen / s.total) : 0;
  if (!v) {
    el.className = "fcard done";
    el.innerHTML = `
      <div class="fthumb" aria-hidden="true"><svg viewBox="0 0 24 24" width="40" height="40" fill="none"
        stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><path d="M8 12.5l2.7 2.7L16 9.8"/></svg></div>
      <div class="fc">
        <button class="nm"></button>
        <span class="nx">All ${s.total} watched — nicely done.</span>
        <span class="fbar"><i style="width:100%"></i></span>
        <div class="facts"><button class="fdone">✓ Mark complete</button>
          <button class="link fdrop" title="Take it off your Focus; it goes back to the backlog">Remove</button></div>
      </div>`;
    el.querySelector(".fdone").onclick = () => markDone(s);
  } else {
    el.className = "fcard";
    el.innerHTML = `
      <button class="fthumb" title="Play the next one">
        <img loading="lazy" src="/thumb/${v.id}" alt=""><span class="play">${PLAY}</span></button>
      <div class="fc">
        <button class="nm"></button>
        <span class="nx"></span>
        <span class="fbar"><i style="width:${pct}%"></i></span>
        <div class="facts"><span class="n">${s.seen} of ${s.total} · ${pct}%</span>
          <button class="link fdrop" title="Take it off your Focus; it goes back to the backlog">Remove</button></div>
      </div>`;
    el.querySelector(".nx").textContent = `Next: ${v.title}${v.duration ? " · " + fmtDur(v.duration) : ""}`;
    el.querySelector(".fthumb").onclick = () => open_(v, s.id);
  }
  el.querySelector(".nm").textContent = srcLabel(s);
  el.querySelector(".nm").onclick = () => pick("s:" + s.id);
  el.querySelector(".fdrop").onclick = () => dropFocus(s);
  return el;
}

// The header's way back: the next video of the oldest Focus, from anywhere.
function paintCont() {
  const b = $("#cont");
  const p = state.pins.find(p => p.next);
  b.hidden = !p;
  if (!p) return;
  const s = state.sources.find(x => x.id === p.id);
  $("#contt").textContent = p.next.title;
  b.title = `Play the next one in ${s ? s.name : "your Focus"}`;
  b.onclick = () => open_(p.next, p.id);
}

// The bar over the videos: what you are looking at and what you can do
// with it. Home has its own dashboard instead.
function paintViewbar() {
  const bar = $("#viewbar");
  bar.hidden = isHome();
  if (bar.hidden) return;
  const src = state.source && state.sources.find(s => s.id === state.source);
  const coll = !src && state.collection &&
    state.collections.find(c => String(c.id) === String(state.collection));
  $("#vbname").textContent = src ? `${glyph(src)} ${src.name}`
                           : coll ? `✦ ${coll.name}` : "Everything";
}

function paintViewProgress() {
  const it = state.source ? state.sources.find(s => s.id === state.source)
           : state.collection ? state.collections.find(c => String(c.id) === String(state.collection))
           : null;
  const box = $("#viewprog");
  box.hidden = !it || !it.total;
  if (box.hidden) return;
  box.querySelector("i").style.width = `${100 * it.seen / it.total}%`;
  box.lastChild.textContent = `✓ ${it.seen} of ${it.total} watched`;
}

// A tick or an undo changes the count without a full reload.
function bumpSeen(by) {
  const it = state.source ? state.sources.find(s => s.id === state.source)
           : state.collections.find(c => String(c.id) === String(state.collection));
  if (it) it.seen = Math.max(0, it.seen + by);
  paintViewProgress();
}

function paintFold() {
  $("#foldbar").setAttribute("aria-expanded", String(foldOpen));
  $("#foldhint").textContent = foldOpen ? "hide" : "show";
  $("#seenlist").hidden = !foldOpen;
}
$("#foldbar").onclick = () => {
  foldOpen = !foldOpen;
  try { localStorage.setItem("flamingo.watched-open", foldOpen ? "1" : "0"); }
  catch (e) {}
  paintFold();
};

// One line apiece down in the fold: enough to recognise a video and send it
// back up, without the full card's worth of buttons you are done with.
function seenRow(v) {
  const el = document.createElement("div");
  el.className = "srow";
  el.innerHTML = `<img loading="lazy" src="/thumb/${v.id}" alt="">
                  <span class="t"></span>
                  <span class="d">${v.duration ? fmtDur(v.duration) : ""}</span>`;
  el.querySelector(".t").textContent =
    v.unavailable ? "Private or deleted video" : v.title;
  el.title = v.unavailable ? v.id : v.title;
  if (v.have || !v.unavailable) el.onclick = () => open_(v);
  else el.style.cursor = "default";

  if (ON_DEVICE && !v.have && !v.unavailable) {
    const get = document.createElement("button");
    get.className = "act icon"; get.textContent = "↓";
    get.title = "Download";
    get.onclick = e => { e.stopPropagation(); grab(v, get); };
    el.appendChild(get);
  }
  // Not a tick: every row down here is already watched, so a checkmark would
  // read as "mark this done" when the only thing it does is undo that.
  const back = document.createElement("button");
  back.className = "act icon"; back.textContent = "↺";
  back.title = "Put back in the queue — marks it not watched";
  back.onclick = async e => {
    e.stopPropagation();
    const r = await post(`/api/watched/${v.id}`);
    v.watched = r.watched;
    bumpSeen(r.watched ? 1 : -1);
    moveOut(el, "rising");
  };
  el.appendChild(back);
  return el;
}

function card(v, seq) {
  const el = document.createElement("div");
  el.className = "card" + (v.watched ? " watched" : "")
                        + (v.unavailable ? " gone" : "");
  // Streamed videos keep their place too, so any video can show a bar.
  const pct = v.watched ? 100 : v.duration && v.progress
              ? Math.min(100, 100 * v.progress / v.duration) : 0;
  el.innerHTML = `
    <div class="thumbwrap">
      <img loading="lazy" src="/thumb/${v.id}" alt="">
      ${seq ? `<span class="seq">${seq}</span>` : ''}
      ${v.have ? `<span class="flag have" style="${seq ? 'left:auto;right:6px' : ''}">${v.audio ? "audio ♪" : "on disk"}</span>` : ''}
      ${v.starred ? '<span class="flag" style="left:auto;right:6px;top:6px">★</span>' : ''}
      ${v.duration ? `<span class="badge">${fmtDur(v.duration)}</span>` : ''}
      ${v.marks ? `<span class="flag" style="top:auto;bottom:6px" title="${v.marks} marked spot${v.marks > 1 ? "s" : ""}">🔖 ${v.marks}</span>` : ''}
      <span class="flag seen" title="Watched" ${v.watched ? '' : 'hidden'}>✓</span>
      ${pct ? `<div class="bar"><i style="width:${pct}%"></i></div>` : ''}
    </div>
    <div class="meta">
      <div class="title"></div>
      <div class="sub"></div>
      <div class="row"></div>
    </div>`;
  // An unavailable entry has no title of its own -- the id stood in for one.
  el.querySelector(".title").textContent =
    v.unavailable ? "Private or deleted video" : v.title;
  el.querySelector(".sub").textContent = v.unavailable
    ? `${v.id} · nothing to play`
    : [v.channel, fmtDate(v), v.have ? fmtSize(v.size) : null].filter(Boolean).join(" · ");

  const row = el.querySelector(".row");
  // Watch is always the main button: it plays the file on disk when there
  // is one and streams otherwise. Downloading is in the ⋯ menu.
  const main = document.createElement("button");
  main.className = "act primary";
  main.textContent = "▶ Watch";
  main.title = v.have ? "Play the copy on this computer" : "Stream it — nothing is saved to disk";
  main.onclick = () => open_(v);
  main.disabled = v.unavailable && !v.have;
  row.appendChild(main);

  // Everything else lives behind one ⋯ button, so the card stays calm.
  const more = document.createElement("button");
  more.className = "act icon more"; more.textContent = "⋯";
  more.title = "More: download, star, watched, collections, hide";
  more.onclick = e => { e.stopPropagation(); cardMenu(v, el, more); };
  row.appendChild(more);

  // Looking costs nothing now: a click on something not on disk streams it.
  const openThumb = () => v.unavailable && !v.have ? null : open_(v);
  el.querySelector(".thumbwrap").onclick = openThumb;
  el.querySelector(".title").onclick = openThumb;
  return el;
}

async function grab(v, btn, audio, height) {
  const label = btn.textContent;
  btn.disabled = true; btn.textContent = btn.classList.contains("icon") ? "…" : "Queued…";
  dismissed.delete(v.id);
  const body = {audio: !!audio, title: v.title};
  if (height != null) body.height = height;
  try { await post(`/api/get/${v.id}`, body); pollJobs(); }
  catch (e) { btn.disabled = false; btn.textContent = label; alert(e.message); }
}

// Each height is a ceiling: a video without 1440p comes down at the next
// size below. Above 1080p the file is VP9 or AV1 -- sharper text in screen
// recordings, heavier to play on an old machine.
const DL_QUALITIES = [["Best available", 0], ["4K (2160p)", 2160], ["1440p", 1440],
                      ["1080p", 1080], ["720p", 720], ["480p", 480]];
// The ⋯ menu on a card. Download sizes sit in one compact row; the rest
// are plain lines. Each line says what it will do, not what the video is.
function cardMenu(v, el, anchor) {
  if ($("#qmenu")?.dataset.vid === v.id) { closeQualityMenu(); return; }
  closeQualityMenu();
  const m = document.createElement("div");
  m.id = "qmenu"; m.dataset.vid = v.id;
  const item = (label, title, fn, cls) => {
    const b = document.createElement("button");
    b.textContent = label; b.title = title || "";
    if (cls) b.className = cls;
    b.onclick = e => { e.stopPropagation(); closeQualityMenu(); fn(); };
    m.appendChild(b);
    return b;
  };
  const head = text => {
    const h = document.createElement("div");
    h.className = "qh"; h.textContent = text; m.appendChild(h);
  };

  if (ON_DEVICE && !v.unavailable) {
    if (!v.have) item("↓ Download", "Best quality up to this computer's limit",
                      () => grab(v, anchor), "get");
    head(v.have ? "Download again at… (replaces the file)" : "…or at a size");
    const sizes = document.createElement("div");
    sizes.className = "qsizes";
    for (const [label, h] of DL_QUALITIES) {
      const b = document.createElement("button");
      b.textContent = label.replace(" available", "").replace(/ \(2160p\)/, "");
      b.title = label;
      b.onclick = e => { e.stopPropagation(); closeQualityMenu(); grab(v, anchor, false, h); };
      sizes.appendChild(b);
    }
    m.appendChild(sizes);
    if (!v.have) item("♪ Audio only (m4a)", "A fraction of the size", () => grab(v, anchor, true));
    m.appendChild(document.createElement("hr"));
  }

  item(v.starred ? "★ Unstar" : "☆ Star", "", async () => {
    const r = await post(`/api/star/${v.id}`);
    v.starred = r.starred;
    refresh();
  });
  item(v.watched ? "↺ Put back in the queue" : "✓ Mark as watched", "", async () => {
    const r = await post(`/api/watched/${v.id}`);
    v.watched = r.watched;
    bumpSeen(r.watched ? 1 : -1);
    el.classList.toggle("watched", !!v.watched);
    el.querySelector(".flag.seen").hidden = !v.watched;
    // The Unwatched filter is showing a list this video may have just left.
    if (state.unwatched) { refresh(); return; }
    // Otherwise it sinks out of the grid and into the fold at the bottom.
    if (!state.ordered) moveOut(el, "sinking");
  });
  item("＋ Add to a collection", "", () => openModal(v));
  item(v.hidden ? "◉ Unhide" : "⊘ Hide from every view", "", async () => {
    const r = await post(`/api/hidden/${v.id}`);
    v.hidden = r.hidden;
    // Either way it no longer belongs in the list it is sitting in.
    refresh();
  });
  if (v.have) item("✕ Delete the file", "The catalog entry stays, so you can download it again", async () => {
    if (!confirm(`Delete the downloaded file for:\n\n${v.title}\n\n` +
                 `The catalog entry stays, so you can re-download later.`)) return;
    await post(`/api/remove/${v.id}`); refresh();
  }, "bad");

  document.body.appendChild(m);
  const r = anchor.getBoundingClientRect();
  m.style.left = `${Math.max(8, Math.min(r.right - m.offsetWidth, innerWidth - m.offsetWidth - 8))}px`;
  m.style.top = r.bottom + m.offsetHeight + 8 > innerHeight
    ? `${Math.max(8, r.top - m.offsetHeight - 4)}px` : `${r.bottom + 4}px`;
}
function closeQualityMenu() { $("#qmenu")?.remove(); }
document.addEventListener("click", e => { if (!e.target.closest("#qmenu")) closeQualityMenu(); });
addEventListener("scroll", closeQualityMenu, {passive: true});

/* ---- player ---- */
// The author's description: its links open in a new tab and its timestamps
// (1:23, 1:02:03) jump the player there. Built from text nodes, never HTML,
// so nothing in a description can run on the page.
const DESC_RE = /(https?:\/\/[^\s<>"]*[^\s<>"'.,;:!?)\]])|(\b(?:\d{1,2}:)?\d{1,2}:\d{2}\b)/g;
function showDesc(text) {
  const box = $("#pdesc");
  box.replaceChildren();
  $("#pdescbox").hidden = !text;
  let last = 0;
  for (const m of text.matchAll(DESC_RE)) {
    box.append(text.slice(last, m.index));
    if (m[1]) {
      const a = document.createElement("a");
      a.href = m[1]; a.target = "_blank"; a.rel = "noopener noreferrer";
      a.textContent = m[1];
      box.append(a);
    } else {
      const b = document.createElement("button");
      b.className = "ts";
      b.textContent = m[2];
      const at = m[2].split(":").reduce((t, x) => t * 60 + Number(x), 0);
      b.title = "Jump here";
      b.onclick = () => { const vid = $("#pvideo"); vid.currentTime = at; vid.play().catch(()=>{}); };
      box.append(b);
    }
    last = m.index + m[0].length;
  }
  box.append(text.slice(last));
}
let current = null, saveTimer = null;
// Share of a video past which it counts as finished.
const FINISHED = 0.92;
function open_(v, from) {
  // Resuming a finished video lands on its last frame, which fires "ended"
  // and shuts the player straight away, so start those over instead.
  const at = v.duration && v.progress / v.duration >= FINISHED ? 0 : v.progress || 0;
  current = v;
  const vid = $("#pvideo");
  vid.querySelectorAll("track").forEach(t => t.remove());
  stopStream();
  $("#ptitle").textContent = v.title;
  $("#psub").textContent = [v.channel, fmtDate(v), fmtDur(v.duration)]
                             .filter(Boolean).join(" · ");
  showDesc(v.description || "");
  // Quick syncs list videos without one: ask for it the first time.
  if (!v.description)
    api(`/api/description/${v.id}`).then(d => {
      if (current?.id !== v.id || !d.description) return;
      v.description = d.description;
      showDesc(d.description);
    }).catch(() => {});
  showChapters([], v.id);
  showMarks([], v.id);
  api(`/api/marks/${v.id}`).then(d => showMarks(d.marks, v.id)).catch(() => {});
  togglMount(v, from);
  $("#player").classList.add("on");
  vid.onerror = null;
  if (v.have) {
    // The file can be gone from the folder since the list was made: then
    // say so and offer to fetch it again, or to stream it.
    vid.onerror = () => { if (current?.id === v.id) fileMissing(v); };
    vid.src = `/media/${v.id}`;
    vid.currentTime = at;
    vid.play().catch(()=>{});
    attachSubs(v.id, vid);
    api(`/api/chapters/${v.id}`).then(d => showChapters(d.chapters, v.id))
                                .catch(() => {});
  } else {
    startStream({...v, progress: at}, vid).catch(e => {
      if (current?.id === v.id) streamStatus(`Couldn't stream: ${e.message}`);
    });
  }
  clearInterval(saveTimer);
  saveTimer = setInterval(() => {
    if (!current || vid.paused) return;
    post(`/api/progress/${current.id}`, {value: vid.currentTime}).catch(()=>{});
    markIfFinished(vid);
  }, 5000);
}
// Tracking is per channel, not per video: one entry that says which creator
// you spent the time on. The start itself is made by the server -- this only
// ever calls localhost, which is what keeps the page sealed.
let tgRunning = null, tgReady = false;

async function togglPoll() {
  try {
    const d = await api("/api/toggl/status");
    tgReady = d.configured;
    tgRunning = d.running && d.running.id ? d.running : null;
  } catch (e) { tgReady = false; }
  paintToggl_();
}

function togglMount(v, from) {
  togglUnmount();
  if (!tgReady) return;
  const chan = (v.channel || "").trim();
  const m = state.toggl[chan] || {};
  const sid = from || state.source;
  const src = sid && state.sources.find(s => s.id === sid);
  const el = document.createElement("div");
  el.id = "ptoggl";
  el.innerHTML = '<button id="tgbtn"></button><span id="tgwhat"></span>';
  el.dataset.videoId = v.id;
  el.dataset.source = sid || "";
  // What this click would file, spelled out before you click it -- the same
  // rules the server applies: a name you set wins, else the playlist being
  // watched names the entry; the channel always rides along as a tag.
  el.dataset.label = m.name || (src && src.kind !== "channel" && src.name) || chan;
  el.dataset.project = m.project || "";
  const seen = new Set();
  // Spelled as the server sends them (library.TogglTag): lowercase, except
  // Youtube and Youtube:<Channel>.
  el.dataset.tags = [...(m.tags || "").split(","), "Youtube", chan && `Youtube:${chan}`]
    .map(t => (t || "").trim())
    .map(t => /^youtube$/i.test(t) ? "Youtube" : /^youtube:/i.test(t) ? "Youtube:" + t.slice(8) : t.toLowerCase())
    .filter(t => t && !seen.has(t.toLowerCase()) && seen.add(t.toLowerCase()))
    .join(", ");
  $("#psub").after(el);
  $("#tgbtn").onclick = togglClick;
  paintToggl_();
  togglPoll();
}

// The timer can be stopped or started in Toggl itself, so the button asks
// again whenever you come back to this tab, and every few minutes while a
// player is open. Toggl's free plan allows about 30 requests an hour.
const TG_EVERY = 3 * 60 * 1000;
setInterval(() => { if ($("#ptoggl") && !document.hidden) togglPoll(); }, TG_EVERY);
document.addEventListener("visibilitychange", () => {
  if (!document.hidden && $("#ptoggl")) togglPoll();
});

function togglUnmount() {
  const el = $("#ptoggl");
  if (el) el.remove();
}

function paintToggl_() {
  const el = $("#ptoggl");
  if (!el) return;
  el.hidden = !tgReady;
  const btn = $("#tgbtn");
  btn.textContent = tgRunning ? "■ Stop Toggl" : "▶ Start Toggl";
  btn.classList.toggle("on", !!tgRunning);
  $("#tgwhat").textContent = tgRunning
    ? `tracking ${tgRunning.description || "—"}`
    : [el.dataset.label, el.dataset.project, el.dataset.tags]
        .filter(Boolean).join(" · ");
}

async function togglClick() {
  const el = $("#ptoggl"), btn = $("#tgbtn");
  btn.disabled = true;
  try {
    const r = tgRunning
      ? await post("/api/toggl/stop", {id: tgRunning.id,
                                       workspace: tgRunning.workspace_id})
      : await post("/api/toggl/start", {video: el.dataset.videoId,
                                        source: el.dataset.source});
    tgRunning = r.running && r.running.id ? r.running : null;
    if (r.project_missing)
      alert(`Started, but Toggl has no project called "${r.project_missing}" — ` +
            `the entry has no project. Create it in Toggl, or fix the name in ` +
            `the toggl sheet.`);
  } catch (e) { alert(e.message); }
  btn.disabled = false;
  paintToggl_();
}

/* ---- streaming: watch without downloading ----
   YouTube sends picture and sound as two separate files. Each opens with a
   small index (the `sidx` box) that says where every few seconds of it start,
   like a book's table of contents. The page reads that, then asks this server
   for only the pieces around where you are watching and hands them to the
   <video> through MediaSource. Every byte still goes through this server. */
const AHEAD = 60, BEHIND = 30;
let stream = null;

function fileMissing(v) {
  $("#pvideo").onerror = null;
  const st = $("#pstatus");
  st.hidden = false;
  st.textContent = "This file is not in this computer's download folder any more. ";
  const again = document.createElement("button");
  again.className = "act get"; again.textContent = "↓ Download again";
  again.onclick = () => grab({...v, have: false}, again);
  const stream = document.createElement("button");
  stream.className = "act primary"; stream.textContent = "▶ Stream it";
  stream.onclick = () => open_({...v, have: false});
  st.append(again, " ", stream);
  refresh();
}

function streamStatus(msg) {
  $("#pstatus").hidden = !msg;
  $("#pstatus").textContent = msg || "";
}

async function rangeBytes(url, lo, hi) {
  const r = await fetch(url, {headers: {Range: `bytes=${lo}-${hi}`}});
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
  return new Uint8Array(await r.arrayBuffer());
}

// A YouTube DASH file is ftyp, moov, sidx, then the media itself. The first
// two are the "init segment" the decoder needs before any piece of media.
async function readIndex(url) {
  let head = new Uint8Array(0), off = 0;
  const need = async n => { if (head.length < n) head = await rangeBytes(url, 0, n + 8191); };
  for (;;) {
    await need(off + 8);
    const dv = new DataView(head.buffer);
    const size = dv.getUint32(off);
    const type = String.fromCharCode(...head.subarray(off + 4, off + 8));
    if (!size) throw new Error("stream has no index");
    if (type !== "sidx") { off += size; continue; }
    await need(off + size);
    const d = new DataView(head.buffer);
    const ver = head[off + 8];
    let p = off + 16;                       // skip version/flags and reference id
    const scale = d.getUint32(p); p += 4;
    let t, first;
    if (ver === 0) { t = d.getUint32(p); first = d.getUint32(p + 4); p += 8; }
    else { t = Number(d.getBigUint64(p)); first = Number(d.getBigUint64(p + 8)); p += 16; }
    const count = d.getUint16(p + 2); p += 4;
    let byte = off + size + first;
    const segs = [];
    for (let i = 0; i < count; i++, p += 12) {
      const len = d.getUint32(p) & 0x7fffffff, dur = d.getUint32(p + 4);
      segs.push({lo: byte, hi: byte + len - 1, start: t / scale, end: (t + dur) / scale});
      byte += len; t += dur;
    }
    return {init: head.slice(0, off), segs};
  }
}

// SourceBuffer work is asynchronous; this turns one call into a promise.
const sbDo = (sb, fn) => new Promise((ok, bad) => {
  const done = () => { sb.removeEventListener("updateend", done);
                       sb.removeEventListener("error", fail); ok(); };
  const fail = e => { sb.removeEventListener("updateend", done);
                      sb.removeEventListener("error", fail); bad(e); };
  sb.addEventListener("updateend", done);
  sb.addEventListener("error", fail);
  try { fn(); } catch (e) { fail(e); }
});
// Seconds already held past `at`, in the stretch that holds `at`.
const heldAhead = (sb, at) => {
  const b = sb.buffered;
  for (let i = 0; i < b.length; i++)
    if (b.start(i) <= at + 0.5 && at < b.end(i)) return b.end(i) - at;
  return 0;
};
// The browser caps each buffer (Chrome: about 150 MB of picture). At 1440p
// and up, a minute ahead plus half a minute behind does not fit. When it is
// full, keep less behind and look less far ahead from then on, then wait for
// playback to use some up. Says whether the piece went in.
async function append(t, bytes, now) {
  for (let trimmed = false; ; trimmed = true) {
    try { await sbDo(t.sb, () => t.sb.appendBuffer(bytes)); return true; }
    catch (e) {
      if (e.name !== "QuotaExceededError") throw e;
      t.behind = 5;
      if (!trimmed && now > 5) {
        await sbDo(t.sb, () => t.sb.remove(0, now - 5));
        continue;
      }
      const ahead = heldAhead(t.sb, now);
      if (ahead < 5) throw e;              // nothing to play through: truly stuck
      t.ahead = Math.max(10, ahead * 0.8);
      return false;
    }
  }
}
const buffered = (sb, s) => {
  const mid = (s.start + s.end) / 2, b = sb.buffered;
  for (let i = 0; i < b.length; i++) if (b.start(i) <= mid && mid < b.end(i)) return true;
  return false;
};

// The quality you last picked, as a ceiling: a video without that height
// plays at the tallest one below it. Nothing picked means config's max_height.
let quality = null;
try { quality = parseInt(localStorage.getItem("flamingo.quality")) || null; } catch (e) {}

function paintQuality(v, heights, h) {
  const box = $("#pquality");
  box.replaceChildren(...heights.map(x => {
    const b = document.createElement("button");
    b.textContent = x + "p";
    b.classList.toggle("on", x === h);
    b.onclick = () => {
      quality = x;
      try { localStorage.setItem("flamingo.quality", x); } catch (e) {}
      if (x !== h) switchQuality(v);
    };
    return b;
  }));
  box.hidden = false;
}

// Start over at the same moment, keeping play/pause as it was.
function switchQuality(v) {
  const vid = $("#pvideo"), paused = vid.paused;
  stopStream();
  startStream({...v, progress: vid.currentTime}, vid, paused).catch(e => {
    if (current?.id === v.id) streamStatus(`Couldn't stream: ${e.message}`);
  });
}

async function startStream(v, vid, paused = false) {
  const me = stream = {alive: true, tracks: []};
  streamStatus("Finding the stream…");
  const meta = await api(`/api/stream/${v.id}`);
  // Above 1080p the picture is AV1, which some browsers cannot decode;
  // those heights are simply not offered.
  const heights = Object.keys(meta.videos).map(Number).sort((a, b) => a - b)
                        .filter(x => MediaSource.isTypeSupported(meta.videos[x]));
  if (!heights.length) throw new Error("this browser cannot play any of its video");
  if (!MediaSource.isTypeSupported(meta.audio))
    throw new Error(`this browser cannot play ${meta.audio}`);
  const fit = heights.filter(x => x <= (quality || meta.default));
  const h = fit.length ? fit[fit.length - 1] : heights[0];
  meta.video = meta.videos[h];
  if (!me.alive) return;
  paintQuality(v, heights, h);
  showChapters(meta.chapters, v.id);
  const ms = me.ms = new MediaSource();
  me.objectUrl = URL.createObjectURL(ms);
  vid.src = me.objectUrl;
  await new Promise(r => ms.addEventListener("sourceopen", r, {once: true}));
  const idx = await Promise.all(["video", "audio"].map(async kind => {
    const url = kind === "video" ? `/stream/${v.id}/video/${h}` : `/stream/${v.id}/audio`;
    return {url, kind, ...(await readIndex(url))};
  }));
  if (!me.alive) return;
  // Both buffers must exist before either gets data: once the first one
  // has its init segment, Chrome will not accept a second.
  me.tracks = idx.map(i => ({url: i.url, sb: ms.addSourceBuffer(meta[i.kind]),
                             segs: i.segs, init: i.init, busy: false,
                             ahead: AHEAD, behind: BEHIND, held: null}));
  await Promise.all(me.tracks.map(t => sbDo(t.sb, () => t.sb.appendBuffer(t.init))));
  if (!me.alive) return;
  if (meta.duration) ms.duration = meta.duration;
  streamStatus("Streaming — nothing is saved to disk.");

  const pump = async t => {
    if (t.busy || !me.alive) return;
    t.busy = true;
    try {
      while (me.alive) {
        const now = vid.currentTime;
        // Let go of what is well behind, so an hour-long video does not
        // pile up in memory.
        const b = t.sb.buffered;
        if (b.length && b.start(0) < now - t.behind - 10)
          await sbDo(t.sb, () => t.sb.remove(0, now - t.behind));
        const seg = t.segs.find(s => s.end > now && s.start < now + t.ahead
                                     && !buffered(t.sb, s));
        if (!seg) break;
        // A piece that did not fit last time is kept, not fetched again.
        const bytes = t.held?.seg === seg ? t.held.bytes
                                          : await rangeBytes(t.url, seg.lo, seg.hi);
        t.held = null;
        if (!me.alive) return;
        if (!(await append(t, bytes, now))) { t.held = {seg, bytes}; break; }
      }
      // Tell the player there is no more once both tracks hold their last piece.
      const done = me.tracks.every(x => buffered(x.sb, x.segs[x.segs.length - 1]));
      if (done && ms.readyState === "open" && me.tracks.every(x => !x.sb.updating))
        ms.endOfStream();
    } catch (e) {
      // A media error is me.fail's to handle; this is everything else.
      if (me.alive && !vid.error) streamStatus(`Stream stalled: ${e.message}`);
    } finally { t.busy = false; }
  };
  // Kept as it goes, because a media error pauses the element on its own.
  me.kick = () => { me.paused = vid.paused; me.tracks.forEach(pump); };
  vid.addEventListener("timeupdate", me.kick);
  vid.addEventListener("seeking", me.kick);
  // A decoder can trip on a stretch it played fine before, and after that
  // the element takes no more data. Start over from the same moment, a few
  // times at most, so one hiccup does not end the video.
  const began = Date.now();
  me.fail = () => {
    if (!me.alive || !vid.error) return;
    const why = vid.error.message || `media error ${vid.error.code}`;
    const tries = Date.now() - began > 60000 ? 1 : (v.tries || 0) + 1;
    if (tries > 3) return streamStatus(`Stream stopped: ${why}`);
    const at = vid.currentTime, wasPaused = me.paused;
    stopStream();
    startStream({...v, progress: at, tries}, vid, wasPaused).catch(e => {
      if (current?.id === v.id) streamStatus(`Couldn't stream: ${e.message}`);
    });
  };
  vid.addEventListener("error", me.fail);
  vid.currentTime = v.progress || 0;
  me.kick();
  me.paused = paused;
  if (!paused) vid.play().catch(() => {});
}

function stopStream() {
  streamStatus("");
  $("#pquality").hidden = true;
  if (!stream) return;
  const vid = $("#pvideo");
  stream.alive = false;
  if (stream.kick) {
    vid.removeEventListener("timeupdate", stream.kick);
    vid.removeEventListener("seeking", stream.kick);
    vid.removeEventListener("error", stream.fail);
  }
  if (stream.objectUrl) URL.revokeObjectURL(stream.objectUrl);
  stream = null;
}

/* ---- speed ---- */
// A new src resets playbackRate to defaultPlaybackRate, so keeping both in
// step is what makes the chosen speed survive from one video to the next.
const SPEEDS = [1, 1.25, 1.5, 1.75, 2];
let speed = 1;
try { speed = parseFloat(localStorage.getItem("flamingo.speed")) || 1; } catch (e) {}
function paintSpeed() {
  for (const b of $("#pspeed").children)
    b.classList.toggle("on", parseFloat(b.dataset.r) === speed);
}
function setSpeed(r) {
  speed = Math.min(4, Math.max(0.25, Math.round(r * 100) / 100));
  const vid = $("#pvideo");
  vid.defaultPlaybackRate = speed;
  if (vid.playbackRate !== speed) vid.playbackRate = speed;
  try { localStorage.setItem("flamingo.speed", speed); } catch (e) {}
  paintSpeed();
}
for (const r of SPEEDS) {
  const b = document.createElement("button");
  b.dataset.r = r; b.textContent = r + "×";
  b.onclick = () => setSpeed(r);
  $("#pspeed").append(b);
}
// Changes from the browser's own ⋮ menu count too.
$("#pvideo").addEventListener("ratechange", e => {
  if (e.target.playbackRate !== speed) setSpeed(e.target.playbackRate);
});
$("#pvideo").addEventListener("loadedmetadata", () => setSpeed(speed));
document.addEventListener("keydown", e => {
  if (!$("#player").classList.contains("on") || e.target.matches?.("input, textarea")) return;
  if (e.key === ">") setSpeed(speed + 0.25);
  else if (e.key === "<") setSpeed(speed - 0.25);
});
setSpeed(speed);

/* ---- marks: "I got this far", as many as you like per video ---- */
let marks = [];
const markJump = t => { const vid = $("#pvideo"); vid.currentTime = t; vid.play().catch(()=>{}); };
function showMarks(list, id, focusId) {
  if (current?.id !== id) return;
  marks = list || [];
  const ol = $("#pmarks");
  ol.replaceChildren();
  for (const m of marks) {
    const li = document.createElement("li");
    const ts = document.createElement("button");
    ts.className = "ts"; ts.textContent = fmtDur(m.t) || "0:00";
    ts.onclick = () => markJump(m.t);
    const note = document.createElement("input");
    note.className = "note"; note.value = m.note; note.placeholder = "add a note…";
    const save = () => { if (note.value.trim() !== m.note)
      markPost({id: m.id, note: note.value}); };
    note.onkeydown = e => { if (e.key === "Enter") note.blur();
                            if (e.key === "Escape") { e.stopPropagation(); note.value = m.note; note.blur(); } };
    note.onblur = save;
    const x = document.createElement("button");
    x.className = "x"; x.textContent = "×"; x.title = "Remove this mark";
    x.onclick = () => markPost({drop: m.id});
    li.append(ts, note, x);
    ol.appendChild(li);
    if (m.id === focusId) setTimeout(() => note.focus(), 0);
  }
  paintMarkStrip();
}
function paintMarkStrip() {
  const strip = $("#pmstrip"), d = $("#pvideo").duration || current?.duration;
  strip.replaceChildren();
  if (!d || !isFinite(d)) return;
  for (const m of marks) {
    const b = document.createElement("button");
    b.style.left = `${Math.min(100, 100 * m.t / d)}%`;
    b.title = `${fmtDur(m.t) || "0:00"}${m.note ? " · " + m.note : ""}`;
    b.onclick = () => markJump(m.t);
    strip.appendChild(b);
  }
}
async function markPost(body) {
  if (!current) return;
  const id = current.id;
  try {
    const r = await post(`/api/marks/${id}`, body);
    const before = new Set(marks.map(m => m.id));
    // A new mark gets its note box focused, ready to say what this spot is.
    const fresh = r.marks.find(m => !before.has(m.id));
    showMarks(r.marks, id, fresh?.id);
  } catch (e) { alert(e.message); }
}
function addMark() {
  if (!current) return;
  markPost({t: Math.floor($("#pvideo").currentTime)});
}
$("#pmarkadd").onclick = addMark;
$("#pvideo").addEventListener("durationchange", paintMarkStrip);
document.addEventListener("keydown", e => {
  if (!$("#player").classList.contains("on") || e.target.matches?.("input, textarea")) return;
  if (e.key === "m" || e.key === "M") addMark();
});

/* ---- chapters ---- */
let chapters = [];
function showChapters(list, id) {
  if (current?.id !== id) return;           // player moved on while we waited
  chapters = list || [];
  const strip = $("#pchap"), ol = $("#pchlist");
  strip.replaceChildren(); ol.replaceChildren();
  strip.hidden = ol.hidden = !chapters.length;
  const jump = t => { const vid = $("#pvideo"); vid.currentTime = t; vid.play().catch(()=>{}); };
  chapters.forEach(c => {
    const ts = fmtDur(c.start) || "0:00";
    const seg = document.createElement("button");
    seg.style.flexGrow = Math.max(c.end - c.start, 1);
    seg.title = `${ts} · ${c.title}`;
    seg.onclick = () => jump(c.start);
    strip.appendChild(seg);
    const li = document.createElement("li"), b = document.createElement("button");
    b.innerHTML = `<span class="ts"></span><span></span>`;
    b.children[0].textContent = ts;
    b.children[1].textContent = c.title;
    b.onclick = () => jump(c.start);
    li.appendChild(b);
    ol.appendChild(li);
  });
  paintChapter();
}
function paintChapter() {
  if (!chapters.length) return;
  const t = $("#pvideo").currentTime, segs = $("#pchap").children,
        items = $("#pchlist").children;
  chapters.forEach((c, i) => {
    const p = Math.min(100, Math.max(0, 100 * (t - c.start) / (c.end - c.start)));
    segs[i].style.setProperty("--p", p + "%");
    const on = c.start <= t && t < c.end;
    if (items[i].classList.contains("on") !== on) {
      items[i].classList.toggle("on", on);
      if (on) items[i].scrollIntoView({block: "nearest"});
    }
  });
}
$("#pvideo").addEventListener("timeupdate", paintChapter);
$("#pvideo").addEventListener("seeked", paintChapter);

async function attachSubs(id, vid) {
  let langs = [];
  try { langs = (await api(`/api/subs/${id}`)).langs || []; } catch (e) { return; }
  if (current?.id !== id) return;           // player moved on while we waited
  // Attached but off: no `default` track, so the subtitles sit in the player's
  // CC menu until you pick one rather than covering the picture uninvited.
  langs.forEach(lang => {
    const t = document.createElement("track");
    t.kind = "subtitles"; t.label = lang; t.srclang = lang.split("-")[0];
    t.src = `/subs/${id}/${encodeURIComponent(lang)}`;
    vid.appendChild(t);
  });
  for (const track of vid.textTracks) track.mode = "disabled";
}

// Credits, an outro, a tab closed on the last thirty seconds: near enough the
// end counts as watched, since "ended" only fires on the very last frame.
function markIfFinished(vid) {
  if (!current || current.watched || !vid.duration) return;
  if (vid.currentTime / vid.duration < FINISHED) return;
  current.watched = true;
  post(`/api/watched/${current.id}`, {value: 1}).catch(()=>{});
}

function close_() {
  const vid = $("#pvideo");
  if (current) post(`/api/progress/${current.id}`, {value: vid.currentTime}).catch(()=>{});
  markIfFinished(vid);
  clearInterval(saveTimer);
  stopStream();
  vid.pause(); vid.removeAttribute("src"); vid.load();
  $("#player").classList.remove("on");
  togglUnmount();
  showChapters([], current?.id);
  current = null; refresh();
}
$("#pclose").onclick = close_;
$("#pvideo").addEventListener("ended", async () => {
  if (current && !current.watched) {
    current.watched = true;
    await post(`/api/watched/${current.id}`, {value: 1});
  }
  close_();
});
document.addEventListener("keydown", e => {
  if (e.key !== "Escape") return;
  if ($("#acctmodal").classList.contains("on")) closeAccount();
  else if ($("#dlmodal").classList.contains("on")) closeDownloads();
  else if ($("#tgmodal").classList.contains("on")) closeToggl();
  else if ($("#srcmodal").classList.contains("on")) closeSrc();
  else if ($("#modal").classList.contains("on")) closeModal();
  else if ($("#player").classList.contains("on")) close_();
  else dismissAllJobs();
});

/* ---- downloads sheet ----
   Each system (Linux, macOS, Windows) has one default folder, kept on the
   server; a computer may use its own instead. The agent on each computer
   reads the answer from there, and finds files by the [id] in their names. */
function dlError(msg) { $("#dlerr").hidden = !msg; $("#dlerr").textContent = msg || ""; }
function closeDownloads() { $("#dlmodal").classList.remove("on"); }

async function openDownloads() {
  dlError("");
  let d;
  try { d = await api("/api/downloads"); } catch (e) { alert(e.message); return; }
  const dev = d.device;
  $("#dlthis").hidden = !dev;
  $("#dlhint").textContent = dev
    ? "Files are found by the [video id] in their names, anywhere inside the " +
      "folder. Point this computer somewhere else and it looks there instead: " +
      "anything left in the old folder shows as not downloaded."
    : "This page came straight from the server, so it can only stream. To " +
      "download, open Flamingo through this computer's agent at " +
      "http://127.0.0.1:8421 (set it up once with: flamingo agent pair).";
  if (dev) {
    const plat = d.platforms.find(p => p.code === dev.platform);
    $("#dlname").textContent = dev.device + (plat ? ` (${plat.name})` : "");
    $("#dldir").value = dev.own ? dev.media_dir : "";
    $("#dldir").placeholder = plat ? `${plat.media_dir} — the ${plat.name} default` : dev.media_dir;
    $("#dlnote").textContent = dev.own
      ? "Its own folder. Empty it and save to go back to the default."
      : "Using the default for its system. Type a folder to give it its own.";
  }
  const box = $("#dlplats");
  box.replaceChildren();
  for (const p of d.platforms) {
    const row = document.createElement("div");
    row.className = "mk dlrow";
    row.innerHTML = `<span class="dlplat"></span><input spellcheck="false" autocomplete="off">
      <button class="act">Save</button>`;
    row.querySelector(".dlplat").textContent = p.name;
    const inp = row.querySelector("input"), btn = row.querySelector("button");
    inp.value = p.media_dir;
    inp.disabled = btn.disabled = !d.admin;
    if (!d.admin) btn.title = inp.title = "Only an admin can change the defaults";
    btn.onclick = async () => {
      dlError("");
      btn.disabled = true;
      try { await post("/api/downloads/platform", {code: p.code, media_dir: inp.value}); btn.textContent = "Saved"; }
      catch (e) { dlError(e.message); }
      finally { btn.disabled = false; setTimeout(() => btn.textContent = "Save", 1500); }
      refresh();
    };
    box.appendChild(row);
  }
  $("#dlmodal").classList.add("on");
}

$("#dlsave").onclick = async () => {
  dlError("");
  const btn = $("#dlsave");
  btn.disabled = true;
  try {
    await post("/api/downloads/device", {media_dir: $("#dldir").value});
    await openDownloads();
    refresh();
  } catch (e) { dlError(e.message); }
  finally { btn.disabled = false; }
};
$("#dlcfg").onclick = openDownloads;
$("#dldone").onclick = closeDownloads;
$("#dlmodal").onclick = e => { if (e.target.id === "dlmodal") closeDownloads(); };

/* ---- toggl sheet ----
   With a channel or playlist open it shows just that creator's row, so
   setting up what you are watching needs no hunting. "All channels" lists
   every creator, with a filter. */
let tgTimer = null, tgAll = false;
const tgOpenSource = () => state.source && state.sources.find(s => s.id === state.source);
function openToggl() {
  tgAll = !tgOpenSource();
  $("#tgfilter").value = "";
  paintToggl();
  // Say so up front rather than letting them fill in seven rows and then
  // wonder why no button ever turns up.
  $("#tgoff").hidden = tgReady;
  $("#tgmodal").classList.add("on");
}

/* ---- your account ----
   Opened from your name in the header: who you are, and your own keys. */
function openAccount() {
  $("#tgtoken").value = ""; tgErr("");
  $("#acctmodal").classList.add("on");
  api("/api/toggl/account").then(paintTgAccount, e => tgErr(e.message));
  $("#tgtoken").focus();
}
function closeAccount() { $("#acctmodal").classList.remove("on"); }
$("#mebtn").onclick = openAccount;
$("#acctdone").onclick = closeAccount;
$("#acctmodal").onclick = e => { if (e.target.id === "acctmodal") closeAccount(); };
$("#tgtoacct").onclick = () => { closeToggl(); openAccount(); };

/* The person's own Toggl key. It goes in, never comes back out: the page
   only learns whether one is stored. */
function paintTgAccount(a) {
  $("#tgstate").textContent = a.connected ? "· connected" : "· not connected";
  $("#tgstate").className = "tgstate" + (a.connected ? " on" : "");
  $("#tgws").value = a.workspace ? a.workspace : "";
  $("#tgtoken").placeholder = a.connected ? "New API token (replaces the stored one)" : "API token";
  $("#tgsave").textContent = a.connected ? "Replace" : "Connect";
  $("#tgclear").hidden = !a.connected;
  $("#tgform").hidden = !a.can_store;
  if (!a.can_store) tgErr("This server has no secret key, so it cannot keep Toggl tokens. Ask an admin.");
  tgReady = a.connected;
  $("#tgoff").hidden = tgReady;
}
function tgErr(msg) { $("#tgerr").textContent = msg; $("#tgerr").hidden = !msg; }
async function tgSend(method, payload) {
  tgErr("");
  $("#tgsave").disabled = $("#tgclear").disabled = true;
  try {
    const d = await api("/api/toggl/account", {method,
      headers: {"Content-Type": "application/json"},
      body: payload ? JSON.stringify(payload) : undefined});
    $("#tgtoken").value = "";
    paintTgAccount(d);
    togglPoll();
  } catch (e) { tgErr(e.message); }
  finally { $("#tgsave").disabled = $("#tgclear").disabled = false; }
}
$("#tgform").onsubmit = e => {
  e.preventDefault();
  const token = $("#tgtoken").value.trim();
  if (!token) { tgErr("Paste your Toggl API token first."); return; }
  const ws = $("#tgws").value.trim();
  tgSend("PUT", ws ? {token, workspace: ws} : {token});
};
$("#tgclear").onclick = () => tgSend("DELETE");
function closeToggl() {
  $("#tgmodal").classList.remove("on");
  refresh(); togglPoll();
}

function paintToggl() {
  const box = $("#tgrows"), src = tgOpenSource();
  box.innerHTML = "";
  $("#tgfilter").hidden = !tgAll;
  $("#tgswitch").hidden = !src;
  if (src) $("#tgswitch").textContent = tgAll ? `Only ${src.tgchannel || src.name}`
                                              : "All channels";
  $("#tgscope").textContent = tgAll ? "· all channels" : `· ${src.tgchannel || src.name}`;
  const rows = tgAll
    ? state.togglChannels
    : [state.togglChannels.find(c => c.channel === src.tgchannel)
       || {channel: src.tgchannel || src.name, name: "", project: "", tags: ""}];
  if (!rows.length) {
    box.innerHTML = '<div class="sub">No channels yet — track a source first.</div>';
    return;
  }
  const q = tgAll ? $("#tgfilter").value.trim().toLowerCase() : "";
  for (const c of rows)
    if (!q || [c.channel, c.name].some(x => (x || "").toLowerCase().includes(q)))
      box.appendChild(togglRow(c));
  if (!box.children.length) box.innerHTML = '<div class="sub">Nothing matches.</div>';
}

function togglRow(c) {
  const el = document.createElement("div");
  el.className = "tgrow";
  const head = document.createElement("div");
  head.className = "head";
  const label = document.createElement("div");
  label.className = "ch";
  label.textContent = c.channel;
  label.title = c.channel;
  // Back to the default name.
  const reset = document.createElement("button");
  reset.className = "link";
  reset.textContent = "Use default name";
  head.append(label, reset);
  el.appendChild(head);

  // The name entries get when none is set, shown in the field itself so it
  // reads as already done, not as something left to type. Watched from a
  // playlist it is the playlist's title, so the open playlist shows that.
  const src = !tgAll && tgOpenSource();
  const fromList = src && src.kind === "playlist" && src.section !== "picks";
  const auto = fromList ? src.name : c.channel;
  const autoWhy = fromList ? "Default: this playlist's title"
    : tgAll ? "Default: the channel's name, or the playlist's title when watched from a playlist"
    : "Default: the channel's name";

  const fields = {};
  for (const [key, ph] of [["name", "Name in Toggl"], ["project", "Project"],
                           ["tags", "Tags, comma separated"]]) {
    const inp = document.createElement("input");
    inp.value = c[key] || "";
    inp.placeholder = ph;
    inp.title = ph;
    inp.autocomplete = "off";
    inp.spellcheck = false;
    // Saved as you type rather than behind a Save button: there is one row per
    // channel and nothing here is destructive, so a trip to a button would be
    // the only hard part of the job.
    const save = () => {
      clearTimeout(tgTimer);
      tgTimer = setTimeout(() => saveToggl(c, fields), 500);
    };
    inp.oninput = save;
    fields[key] = inp;
    if (key !== "name") { el.appendChild(inp); continue; }

    const wrap = document.createElement("span");
    wrap.className = "tgname";
    const tag = document.createElement("span");
    tag.className = "tag";
    tag.textContent = "default";
    tag.title = autoWhy;
    wrap.append(inp, tag);
    el.appendChild(wrap);
    const paint = isAuto => {
      inp.classList.toggle("auto", isAuto);
      tag.hidden = !isAuto;       // the tag says it is the default...
      reset.hidden = isAuto;      // ...and the link offers it back once it isn't
      inp.title = isAuto ? autoWhy : ph;
    };
    if (!inp.value.trim()) { inp.value = auto; paint(true); } else paint(false);
    // Typing over the default makes it yours; clearing it, or typing the
    // default back, returns to automatic.
    inp.onfocus = () => { if (inp.classList.contains("auto")) inp.select(); };
    inp.oninput = () => {
      const v = inp.value.trim();
      paint(!v || v === auto);
      save();
    };
    inp.onblur = () => { if (!inp.value.trim()) inp.value = auto; };
    reset.onclick = () => { inp.value = auto; paint(true); save(); };
  }
  return el;
}

async function saveToggl(c, fields) {
  const patch = {channel: c.channel,
                 name: fields.name.classList.contains("auto") ? "" : fields.name.value.trim(),
                 project: fields.project.value, tags: fields.tags.value};
  try {
    const r = await post("/api/toggl", patch);
    Object.assign(c, {name: patch.name, project: patch.project, tags: patch.tags});
    state.togglChannels = r.channels;
    state.toggl = Object.fromEntries(r.channels
      .filter(x => x.name || x.project || x.tags)
      .map(x => [x.channel, {name: x.name, project: x.project, tags: x.tags}]));
    note("saved");
  } catch (e) { note(e.message); }
}
function note(text) {
  $("#tgnote").textContent = text;
  setTimeout(() => { if ($("#tgnote").textContent === text) $("#tgnote").textContent = ""; },
             1600);
}
$("#togglcfg").onclick = openToggl;
$("#tgswitch").onclick = () => { tgAll = !tgAll; $("#tgfilter").value = ""; paintToggl(); };
$("#tgfilter").oninput = () => paintToggl();
$("#tgdone").onclick = closeToggl;
$("#tgmodal").onclick = e => { if (e.target.id === "tgmodal") closeToggl(); };

/* ---- collection modal ---- */
let modalVideo = null;
async function openModal(v) {
  modalVideo = v;
  $("#mwho").textContent = v.title;
  $("#mnew").value = "";
  await paintModal();
  $("#modal").classList.add("on");
  $("#mnew").focus();
}
async function paintModal() {
  const mine = new Set((await api(`/api/collections/${modalVideo.id}`))
                        .collections.map(c => c.id));
  const list = $("#mlist");
  list.innerHTML = state.collections.length ? "" :
    '<div class="sub">No collections yet — make one below.</div>';
  for (const c of state.collections) {
    const lab = document.createElement("label");
    const cb = document.createElement("input");
    cb.type = "checkbox"; cb.checked = mine.has(c.id);
    cb.onchange = async () => {
      await post(`/api/collect/${modalVideo.id}`,
                 {collection: c.id, remove: !cb.checked});
      refresh();
    };
    lab.appendChild(cb);
    lab.appendChild(document.createTextNode(`${c.name} (${c.total})`));
    list.appendChild(lab);
  }
}
$("#madd").onclick = async () => {
  const name = $("#mnew").value.trim();
  if (!name) return;
  const c = await post("/api/collections", {name});
  $("#mnew").value = "";
  if (modalVideo) await post(`/api/collect/${modalVideo.id}`, {collection: c.id});
  const d = await api("/api/state?" + params());
  state.collections = d.collections;
  pickerKey = "";                       // force the picker to pick up the new one
  await paintModal();
  refresh();
};
$("#mnew").addEventListener("keydown", e => { if (e.key === "Enter") $("#madd").click(); });
$("#modal").onclick = e => { if (e.target.id === "modal") closeModal(); };
function closeModal() { $("#modal").classList.remove("on"); modalVideo = null; }
$("#newcoll").onclick = async () => {
  modalVideo = null;
  $("#mwho").textContent = "Create a new collection";
  $("#mlist").innerHTML = "";
  $("#mnew").value = "";
  $("#modal").classList.add("on");
  $("#mnew").focus();
};

/* ---- track a channel or playlist ---- */
// The job this page is waiting on, so a source that finishes cataloguing can
// drop you straight into it -- the terminal's "next: yt list <name>", without
// the typing. Only ever one: a batch of playlists has no single view to land in.
let pendingSource = null;
function closeSrc() { $("#srcmodal").classList.remove("on"); askMode(); }
function srcError(msg) {
  // A rejected handle is worth reading next to the box that produced it, not
  // in an alert that throws away what you typed.
  const box = $("#serr");
  box.textContent = msg;
  box.hidden = false;
}
// Two modes in one sheet: ask for something to track, or pick from what a
// creator has. Browsing replaces the box rather than opening a second window.
function askMode() {
  $("#sask").hidden = false;
  $("#spick").hidden = true;
  $("#serr").hidden = true;
  $("#stitle").textContent = "Track a channel or playlist";
  $("#shint").hidden = false;
}
function pickMode(creator, playlists) {
  $("#sask").hidden = true;
  $("#spick").hidden = false;
  $("#serr").hidden = true;
  $("#shint").hidden = true;
  $("#stitle").textContent = creator;
  const n = playlists.filter(p => !p.tracked).length;
  $("#spickwho").textContent =
    `${playlists.length} to pick from · ${n} not tracked yet`;
  const list = $("#spicks");
  list.innerHTML = "";
  // Courses, live, playlists: the server sends them in that order, so a
  // heading goes in wherever the section changes.
  const heads = {course:"Courses", live:"Live", playlist:"Playlists"};
  let section = null;
  for (const pl of playlists) {
    if (pl.section !== section) {
      section = pl.section;
      const h = document.createElement("div");
      h.className = "sec";
      const count = playlists.filter(p => p.section === section).length;
      h.textContent = `${GLYPHS[section]} ${heads[section]} · ${count}`;
      list.appendChild(h);
    }
    const lab = document.createElement("label");
    const cb = document.createElement("input");
    cb.type = "checkbox";
    cb.value = pl.id;
    cb.dataset.section = pl.section;
    // One you already track is shown, but not on offer: re-adding it would
    // just be a sync, and the point of this list is what you are missing.
    cb.disabled = pl.tracked;
    cb.onchange = paintPickCount;
    const title = document.createElement("span");
    title.textContent = pl.title;
    if (pl.tracked) title.style.opacity = ".55";
    lab.appendChild(cb);
    lab.appendChild(title);
    if (pl.tracked) {
      const tag = document.createElement("span");
      tag.className = "tag";
      tag.textContent = "tracked";
      lab.appendChild(tag);
    }
    list.appendChild(lab);
  }
  paintPickCount();
}
const picked = () => [...$("#spicks").querySelectorAll("input:checked")]
                       .map(cb => ({ref: cb.value, section: cb.dataset.section}));
function paintPickCount() {
  const n = picked().length;
  const btn = $("#spickadd");
  btn.textContent = n ? `Track ${n} selected` : "Track selected";
  btn.disabled = !n;
}
function setAll(on) {
  for (const cb of $("#spicks").querySelectorAll("input:not(:disabled)"))
    cb.checked = on;
  paintPickCount();
}

$("#newsrc").onclick = () => {
  $("#snew").value = "";
  askMode();
  $("#srcmodal").classList.add("on");
  $("#snew").focus();
};
$("#srcmodal").onclick = e => { if (e.target.id === "srcmodal") closeSrc(); };

async function addSource() {
  const ref = $("#snew").value.trim();
  if (!ref) return;
  const btn = $("#sadd");
  btn.disabled = true;
  try {
    const r = await post("/api/sources", {ref});
    pendingSource = r.job.id;
    closeSrc();
    pollJobs();               // the card takes it from here
  } catch (e) {
    srcError(e.message);
    $("#snew").focus();
  } finally {
    btn.disabled = false;
  }
}
async function browseSource() {
  const creator = $("#snew").value.trim();
  if (!creator) { srcError("paste a channel handle (@name) or a channel URL"); return; }
  const btn = $("#sbrowse"), label = btn.textContent;
  // Listing a creator's tabs is a few seconds of yt-dlp, so say that plainly
  // rather than leaving a dead button.
  btn.disabled = true;
  btn.textContent = "listing…";
  $("#serr").hidden = true;
  try {
    const r = await post("/api/sources/browse", {creator});
    pickMode(r.creator, r.playlists);
  } catch (e) {
    srcError(e.message);
  } finally {
    btn.disabled = false;
    btn.textContent = label;
  }
}
async function addPicked() {
  const refs = picked();
  if (!refs.length) return;
  const btn = $("#spickadd");
  btn.disabled = true;
  try {
    const r = await post("/api/sources", {refs});
    // One pick has a view to land in; a batch does not, so the cards report it.
    pendingSource = refs.length === 1 ? r.job.id : null;
    closeSrc();
    pollJobs();
  } catch (e) {
    srcError(e.message);
  } finally {
    paintPickCount();
  }
}
$("#sadd").onclick = addSource;
$("#sbrowse").onclick = browseSource;
$("#sall").onclick = () => setAll(true);
$("#snone").onclick = () => setAll(false);
$("#sback").onclick = askMode;
$("#spickadd").onclick = addPicked;
$("#snew").addEventListener("keydown", e => { if (e.key === "Enter") addSource(); });

/* ---- jobs ---- */
let jobTimer = null, lastJobs = [];
// Cards the user closed. A finished job is dropped server-side too; a running
// one only loses its card, since the download itself keeps going.
const dismissed = new Set();
// Pending auto-dismiss timers for jobs that finished cleanly, keyed by id.
const fading = new Map();
const AUTO_DISMISS_MS = 4000;

function dismissJob(j) {
  dismissed.add(j.id);
  clearTimeout(fading.get(j.id));
  fading.delete(j.id);
  if (j.state !== "queued" && j.state !== "running")
    post("/api/jobs/clear", {id: j.id}).catch(() => {});
  renderJobs(lastJobs);
}
function renderJobs(jobs) {
  lastJobs = jobs;
  // Forget ids the server no longer reports, so a re-download shows up again.
  const live = new Set(jobs.map(j => j.id));
  for (const id of [...dismissed]) if (!live.has(id)) dismissed.delete(id);
  for (const [id, t] of [...fading])
    if (!live.has(id)) { clearTimeout(t); fading.delete(id); }
  // A clean download announces itself and then gets out of the way on its own.
  // Failures stay put: they carry a message worth reading.
  for (const j of jobs)
    if (j.state === "done" && !dismissed.has(j.id) && !fading.has(j.id))
      fading.set(j.id, setTimeout(() => dismissJob(j), AUTO_DISMISS_MS));
  // A job dismissed while running is reaped once it settles.
  for (const j of jobs)
    if (dismissed.has(j.id) && j.state !== "queued" && j.state !== "running")
      post("/api/jobs/clear", {id: j.id}).catch(() => {});

  // A source that has just finished cataloguing is the view you asked for.
  const waited = pendingSource && jobs.find(j => j.id === pendingSource);
  if (waited && waited.state === "done" && waited.result) {
    pendingSource = null;
    state.source = waited.result.source_id;
    state.collection = "";
    refresh({push: true});
  } else if (waited && waited.state === "error") {
    pendingSource = null;
  }

  const box = $("#jobs");
  box.innerHTML = "";
  const shown = jobs.filter(j => !dismissed.has(j.id));
  if (shown.length > 1) {
    const all = document.createElement("button");
    all.className = "clearall";
    all.textContent = `Dismiss all (${shown.length})`;
    all.title = "Close every card (downloads in progress keep running)";
    all.onclick = () => shown.forEach(dismissJob);
    box.appendChild(all);
  }
  for (const j of shown) {
    const el = document.createElement("div");
    el.className = "job" + (j.state === "error" ? " error" : "");
    const pct = Math.round(100 * (j.fraction || 0));
    el.innerHTML = `<button class="x" title="Dismiss">✕</button>
      <div class="t"></div><div class="d"></div>
      <div class="track"><i style="width:${pct}%"></i></div>`;
    el.querySelector(".t").textContent =
      j.kind === "source" ? `＋ ${j.title}` : j.title;
    el.querySelector(".d").textContent =
      j.state === "error" ? j.error : (j.state === "done" ? "done" : j.detail);
    el.querySelector(".x").onclick = e => { e.stopPropagation(); dismissJob(j); };
    box.appendChild(el);
  }
  const busy = jobs.some(j => j.state === "queued" || j.state === "running");
  clearInterval(jobTimer);
  if (busy) jobTimer = setInterval(pollJobs, 1200);
}
function dismissAllJobs() {
  lastJobs.filter(j => !dismissed.has(j.id)).forEach(dismissJob);
}
async function pollJobs() {
  const d = await api("/api/jobs");
  const wasBusy = d.jobs.some(j => j.state === "queued" || j.state === "running");
  renderJobs(d.jobs);
  if (!wasBusy) refresh();
}

/* ---- filters ---- */
let debounce = null;
$("#q").addEventListener("input", e => {
  state.q = e.target.value.trim();
  clearTimeout(debounce); debounce = setTimeout(refresh, 220);
});
$("#sort").addEventListener("change", async e => {
  // Persisted against the view you were looking at when you chose it.
  await post("/api/sort", {sort: e.target.value, source: state.source || null,
                           collection: state.collection || null});
  refresh();
});
function toggle(id, key) {
  const b = $(id);
  b.onclick = () => {
    const on = b.getAttribute("aria-pressed") === "true";
    b.setAttribute("aria-pressed", String(!on));
    if (key === "have") state.have = on ? null : true; else state[key] = !on;
    refresh();
  };
}
toggle("#f-have", "have");
if (!ON_DEVICE) $("#f-have").hidden = true;
toggle("#f-starred", "starred"); toggle("#f-unwatched", "unwatched");
toggle("#f-hidden", "hidden");

function restoreFromUrl() {
  const p = new URLSearchParams(location.search);
  state.q = p.get("q") || "";
  state.source = p.get("source") || "";
  state.collection = p.get("collection") || "";
  state.have = ON_DEVICE && p.has("have") ? p.get("have") === "1" : null;
  state.starred = p.get("starred") === "1";
  state.unwatched = p.get("unwatched") === "1";
  state.hidden = p.get("hidden") === "1";
  state.all = p.get("all") === "1";
  $("#q").value = state.q;
  $("#f-have").setAttribute("aria-pressed", String(state.have === true));
  for (const [id, on] of [["#f-starred", state.starred],
                          ["#f-unwatched", state.unwatched],
                          ["#f-hidden", state.hidden]])
    $(id).setAttribute("aria-pressed", String(on));
}
restoreFromUrl();
togglPoll();
// Back and forward move between views rather than out of the library.
addEventListener("popstate", () => { restoreFromUrl(); refresh(); });

refresh();
