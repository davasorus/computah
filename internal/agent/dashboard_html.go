// Dashboard HTML — served at /. A single self-contained page: no build step,
// no external assets, inline CSS/JS. Instrument-panel aesthetic: dark,
// monospace, dense; the live event stream is the centerpiece.
package agent

const dashHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>agent · live</title>
<style>
  :root {
    --bg: #0b0e14;
    --panel: #11151f;
    --line: #1c2230;
    --ink: #c7d0e0;
    --dim: #5b6577;
    --cyan: #56b6c2;
    --amber: #d19a66;
    --green: #98c379;
    --red: #e06c75;
    --violet: #c678dd;
    --mono: ui-monospace, "SF Mono", "JetBrains Mono", "Cascadia Code", Menlo, Consolas, monospace;
  }
  * { box-sizing: border-box; }
  html, body { margin: 0; height: 100%; }
  body {
    background: var(--bg); color: var(--ink); font-family: var(--mono);
    font-size: 13px; line-height: 1.5; display: grid;
    grid-template-rows: auto 1fr auto; height: 100vh; overflow: hidden;
  }
  #composer { display: flex; gap: 8px; padding: 10px 16px; border-top: 1px solid var(--line); background: var(--panel); }
  #composer input {
    flex: 1; background: var(--bg); border: 1px solid var(--line); color: var(--ink);
    font-family: var(--mono); font-size: 13px; padding: 8px 10px; border-radius: 6px; outline: none;
  }
  #composer input:focus { border-color: var(--cyan); }
  #composer button {
    background: var(--cyan); color: var(--bg); border: none; padding: 8px 16px;
    border-radius: 6px; font-family: var(--mono); font-weight: 600; cursor: pointer;
  }
  #composer button:disabled { opacity: 0.5; cursor: default; }
  /* top strip: identity + stats */
  header {
    border-bottom: 1px solid var(--line); background: var(--panel);
    display: flex; align-items: center; gap: 24px; padding: 10px 16px;
    flex-wrap: wrap;
  }
  .brand { font-weight: 600; letter-spacing: 0.5px; color: var(--ink); }
  .brand b { color: var(--cyan); }
  .live { display: inline-flex; align-items: center; gap: 6px; color: var(--dim); font-size: 11px; }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--dim); }
  .dot.on { background: var(--green); box-shadow: 0 0 6px var(--green); }
  .working { display: inline-flex; align-items: center; gap: 7px; color: var(--dim); font-size: 12px; }
  .working.on { color: var(--amber); }
  .spin { width: 10px; height: 10px; border-radius: 50%; border: 2px solid var(--line); border-top-color: var(--amber); display: none; }
  .working.on .spin { display: inline-block; animation: spin 0.7s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  .stats { display: flex; gap: 20px; margin-left: auto; flex-wrap: wrap; }
  .stat { display: flex; flex-direction: column; }
  .stat .k { color: var(--dim); font-size: 10px; text-transform: uppercase; letter-spacing: 0.6px; }
  .stat .v { color: var(--ink); font-variant-numeric: tabular-nums; }
  /* body: feed + rail */
  main { display: grid; grid-template-columns: 1fr 280px; min-height: 0; }
  #feed {
    overflow-y: auto; padding: 12px 16px; scroll-behavior: smooth;
  }
  .ev { display: flex; gap: 10px; padding: 1px 0; white-space: pre-wrap; word-break: break-word; }
  .ev .t { color: var(--dim); flex: 0 0 auto; font-size: 11px; padding-top: 1px; }
  .ev .b { flex: 1 1 auto; }
  .ev.tool_call .b { color: var(--cyan); }
  .ev.tool_done .b { color: var(--dim); }
  .ev.error .b { color: var(--red); }
  .ev.status .b { color: var(--violet); }
  .ev.stats .b { color: var(--amber); }
  .ev.assistant .b, .ev.token .b { color: var(--ink); }
  .ev.user .b { color: var(--green); }
  .ev.approval { background: rgba(209,154,102,0.08); border-left: 2px solid var(--amber); padding: 4px 8px; margin: 4px 0; }
  .ev.approval .apr-q { color: var(--amber); }
  .ev.approval button { background: var(--panel); color: var(--ink); border: 1px solid var(--line); border-radius: 4px; padding: 2px 8px; margin-left: 4px; cursor: pointer; font-family: var(--mono); font-size: 11px; }
  .ev.approval .apr-y { border-color: var(--green); color: var(--green); }
  .ev.approval .apr-n { border-color: var(--red); color: var(--red); }
  .ev.approval button:disabled { opacity: 0.4; cursor: default; }
  /* rendered markdown inside assistant blocks */
  .md h1, .md h2, .md h3, .md h4 { color: var(--cyan); margin: 8px 0 4px; font-size: 1em; font-weight: 700; }
  .md p { margin: 4px 0; }
  .md ul, .md ol { margin: 4px 0; padding-left: 20px; }
  .md li { margin: 1px 0; }
  .md code { background: var(--bg); color: var(--amber); padding: 1px 4px; border-radius: 3px; }
  .md pre { background: var(--bg); border: 1px solid var(--line); border-radius: 6px; padding: 8px 10px; overflow-x: auto; margin: 6px 0; }
  .md pre code { background: none; color: var(--green); padding: 0; }
  .md blockquote { border-left: 3px solid var(--line); margin: 4px 0; padding-left: 10px; color: var(--dim); }
  .md del { color: var(--dim); }
  .md a { color: var(--cyan); }
  .md table { border-collapse: collapse; margin: 8px 0; font-size: 0.95em; }
  .md th, .md td { border: 1px solid var(--line); padding: 4px 10px; text-align: left; }
  .md th { background: var(--bg); color: var(--cyan); font-weight: 700; }
  .md tbody tr:nth-child(even) { background: rgba(255,255,255,0.02); }
  .glyph { color: var(--dim); }
  aside {
    border-left: 1px solid var(--line); background: var(--panel);
    overflow-y: auto; padding: 12px 14px;
  }
  aside h2 {
    font-size: 10px; text-transform: uppercase; letter-spacing: 0.8px;
    color: var(--dim); margin: 0 0 8px; font-weight: 600;
  }
  #todos { list-style: none; margin: 0 0 20px; padding: 0; }
  #todos li { display: flex; gap: 8px; padding: 3px 0; align-items: baseline; }
  #todos .box { color: var(--dim); flex: 0 0 auto; }
  #todos li.done .box { color: var(--green); }
  #todos li.done .txt { color: var(--dim); text-decoration: line-through; }
  .empty { color: var(--dim); font-style: italic; }
  ::-webkit-scrollbar { width: 10px; height: 10px; }
  ::-webkit-scrollbar-thumb { background: var(--line); border-radius: 6px; }
  ::-webkit-scrollbar-track { background: transparent; }
  @media (max-width: 720px) { main { grid-template-columns: 1fr; } aside { display: none; } }
</style>
</head>
<body>
  <header>
    <span class="brand"><b>agent</b> · live</span>
    <span class="live"><span class="dot" id="dot"></span><span id="conn">connecting…</span></span>
    <span class="working" id="working"><span class="spin" id="spin"></span><span id="worktext">idle</span></span>
    <div class="stats">
      <div class="stat"><span class="k">requests</span><span class="v" id="s-req">—</span></div>
      <div class="stat"><span class="k">sent tk</span><span class="v" id="s-prompt">—</span></div>
      <div class="stat"><span class="k">thought</span><span class="v" id="s-think">—</span></div>
      <div class="stat"><span class="k">gen tk</span><span class="v" id="s-gen">—</span></div>
      <div class="stat"><span class="k">last ttfb</span><span class="v" id="s-ttfb">—</span></div>
    </div>
  </header>
  <main>
    <div id="feed"></div>
    <aside>
      <h2>todos</h2>
      <ul id="todos"><li class="empty">none yet</li></ul>
    </aside>
  </main>
  <form id="composer" style="display:none">
    <input id="prompt" type="text" placeholder="send a prompt to the agent…" autocomplete="off">
    <button type="submit">send</button>
  </form>
<script>
(function () {
  var feed = document.getElementById('feed');
  var dot = document.getElementById('dot'), conn = document.getElementById('conn');
  var working = document.getElementById('working'), worktext = document.getElementById('worktext');
  var atBottom = true;
  var curAsst = null; // current turn's assistant block (reset at turn boundaries)
  feed.addEventListener('scroll', function () {
    atBottom = feed.scrollHeight - feed.scrollTop - feed.clientHeight < 40;
  });

  var glyphs = { tool_call: '⚙', tool_done: '↳', error: '✗', status: '•', stats: '∑', user: '❯', assistant: '', token: '' };

  function add(ev) {
    // Busy lifecycle drives the working indicator.
    if (ev.kind === 'busy') {
      var on = ev.text === '1';
      working.classList.toggle('on', on);
      worktext.textContent = on ? 'working…' : 'idle';
      if (!on) curAsst = null; // turn done — next assistant text is a new block
      return;
    }
    // Thinking updates the working text with live token counts.
    if (ev.kind === 'thinking') {
      conn.textContent = 'connected';
      if (working.classList.contains('on')) {
        worktext.textContent = ev.text || 'working…';
      }
      return;
    }
    // Assistant text arrives line-by-line; coalesce consecutive lines into
    // one block instead of a timestamped row per line.
    if (ev.kind === 'assistant') {
      // Accumulate ALL assistant text from a turn into one block, tracked by
      // reference — NOT by feed.lastChild, because tool-call/stats/busy
      // events interleave with assistant lines and would otherwise split a
      // table's header and separator into separate blocks (so it never
      // renders as a table). curAsst is reset at turn boundaries (user
      // message / busy=idle).
      if (!curAsst) {
        curAsst = document.createElement('div');
        curAsst.className = 'ev assistant';
        curAsst.innerHTML = '<span class="t">' + (ev.time || '') + '</span><span class="b md"></span>';
        curAsst._raw = ev.text || '';
        feed.appendChild(curAsst);
      } else {
        curAsst._raw = (curAsst._raw || '') + '\n' + (ev.text || '');
      }
      curAsst.querySelector('.b').innerHTML = renderMarkdown(curAsst._raw);
      while (feed.childNodes.length > 2000) feed.removeChild(feed.firstChild);
      if (atBottom) feed.scrollTop = feed.scrollHeight;
      return;
    }
    if (ev.kind === 'approval') {
      var id = ev.meta && ev.meta.id;
      var box = document.createElement('div');
      box.className = 'ev approval';
      var tok = ev.tool || '';
      box.innerHTML = '<span class="t">' + (ev.time || '') + '</span>' +
        '<span class="b"><span class="apr-q">approve: ' + escapeHtml(ev.text || '') + '</span> ' +
        '<button class="apr-y">yes</button> ' +
        '<button class="apr-a">always ' + escapeHtml(tok) + '</button> ' +
        '<button class="apr-n">no</button></span>';
      feed.appendChild(box);
      (function () {
        function answer(decision) {
          box.querySelectorAll('button').forEach(function (b) { b.disabled = true; });
          fetch('/api/approve', {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: id, decision: decision })
          }).then(function () {
            box.querySelector('.apr-q').textContent = 'approve: ' + (ev.text || '') + '  → ' + decision;
          });
        }
        box.querySelector('.apr-y').onclick = function () { answer('once'); };
        box.querySelector('.apr-a').onclick = function () { answer('always'); };
        box.querySelector('.apr-n').onclick = function () { answer('deny'); };
      })();
      if (atBottom) feed.scrollTop = feed.scrollHeight;
      return;
    }
    // A user message marks a new turn — the next assistant text is fresh.
    if (ev.kind === 'user') curAsst = null;
    var row = document.createElement('div');
    row.className = 'ev ' + ev.kind;
    var body = ev.text || '';
    if (ev.kind === 'tool_call') {
      body = ev.tool + '(' + (ev.text || '') + ')' + (ev.meta && ev.meta.inline === '1' ? ' [inline]' : '');
    } else if (ev.kind === 'tool_done') {
      body = (ev.tool ? ev.tool + ': ' : '') + body;
    }
    var g = glyphs[ev.kind] || '';
    row.innerHTML = '<span class="t">' + (ev.time || '') + '</span>' +
      '<span class="b">' + (g ? '<span class="glyph">' + g + '</span> ' : '') + escapeHtml(body) + '</span>';
    feed.appendChild(row);
    // cap DOM nodes
    while (feed.childNodes.length > 2000) feed.removeChild(feed.firstChild);
    if (atBottom) feed.scrollTop = feed.scrollHeight;
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>]/g, function (c) {
      return c === '&' ? '&amp;' : c === '<' ? '&lt;' : '&gt;';
    });
  }

  // renderMarkdown: a small, dependency-free markdown→HTML renderer covering
  // what the agent actually emits — headings, bold/italic, inline + fenced
  // code, bullet/numbered lists, blockquotes, and GFM tables. Not a full
  // CommonMark implementation; deliberately compact and safe (everything is
  // escaped before structural HTML is added).
  function renderMarkdown(src) {
    var BT = String.fromCharCode(96); // backtick — built dynamically so it
    // doesn't terminate the Go raw-string literal this HTML lives inside.
    var reInlineCode = new RegExp(BT + '([^' + BT + ']+)' + BT, 'g');
    var reFence = new RegExp('^\\s*' + BT + BT + BT + '(\\w*)');
    var reFenceClose = new RegExp('^\\s*' + BT + BT + BT);
    var lines = String(src).split('\n');
    var out = [];
    var i = 0;
    function inline(t) {
      t = escapeHtml(t);
      t = t.replace(reInlineCode, '<code>$1</code>');
      t = t.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
      t = t.replace(/(^|[^*])\*([^*]+)\*/g, '$1<em>$2</em>');
      t = t.replace(/~~([^~]+)~~/g, '<del>$1</del>');
      t = t.replace(/\[([^\]]+)\]\((https?:[^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
      return t;
    }
    function isTableSep(s) { return /^\s*\|?[\s:|-]+\|[\s:|-]*$/.test(s) && s.indexOf('-') !== -1; }
    function cells(row) {
      var r = row.trim().replace(/^\|/, '').replace(/\|$/, '');
      return r.split('|').map(function (c) { return c.trim(); });
    }
    while (i < lines.length) {
      var line = lines[i];
      // fenced code block
      var fence = line.match(reFence);
      if (fence) {
        var code = [];
        i++;
        while (i < lines.length && !reFenceClose.test(lines[i])) { code.push(lines[i]); i++; }
        i++; // closing fence
        out.push('<pre><code>' + escapeHtml(code.join('\n')) + '</code></pre>');
        continue;
      }
      // table: a header row followed by a separator row
      if (line.indexOf('|') !== -1 && i + 1 < lines.length && isTableSep(lines[i + 1])) {
        var header = cells(line);
        i += 2;
        var rows = [];
        while (i < lines.length && lines[i].indexOf('|') !== -1 && lines[i].trim() !== '') {
          rows.push(cells(lines[i])); i++;
        }
        var t = '<table><thead><tr>';
        header.forEach(function (h) { t += '<th>' + inline(h) + '</th>'; });
        t += '</tr></thead><tbody>';
        rows.forEach(function (r) {
          t += '<tr>';
          for (var c = 0; c < header.length; c++) { t += '<td>' + inline(r[c] || '') + '</td>'; }
          t += '</tr>';
        });
        t += '</tbody></table>';
        out.push(t);
        continue;
      }
      // heading
      var h = line.match(/^\s*(#{1,6})\s+(.*)$/);
      if (h) { var lvl = h[1].length; out.push('<h' + lvl + '>' + inline(h[2]) + '</h' + lvl + '>'); i++; continue; }
      // blockquote
      if (/^\s*>\s?/.test(line)) { out.push('<blockquote>' + inline(line.replace(/^\s*>\s?/, '')) + '</blockquote>'); i++; continue; }
      // list (group consecutive items)
      if (/^\s*[-*+]\s+/.test(line) || /^\s*\d+\.\s+/.test(line)) {
        var ordered = /^\s*\d+\.\s+/.test(line);
        var items = [];
        while (i < lines.length && (/^\s*[-*+]\s+/.test(lines[i]) || /^\s*\d+\.\s+/.test(lines[i]))) {
          items.push(inline(lines[i].replace(/^\s*(?:[-*+]|\d+\.)\s+/, ''))); i++;
        }
        out.push('<' + (ordered ? 'ol' : 'ul') + '>' + items.map(function (it) { return '<li>' + it + '</li>'; }).join('') + '</' + (ordered ? 'ol' : 'ul') + '>');
        continue;
      }
      // blank line
      if (line.trim() === '') { i++; continue; }
      // paragraph (accumulate until blank/structural)
      var para = [line]; i++;
      while (i < lines.length && lines[i].trim() !== '' && !/^\s*(#{1,6}\s|[-*+]\s|\d+\.\s|>)/.test(lines[i]) && !reFence.test(lines[i]) && lines[i].indexOf('|') === -1) {
        para.push(lines[i]); i++;
      }
      out.push('<p>' + inline(para.join(' ')) + '</p>');
    }
    return out.join('\n');
  }

  var es = new EventSource('/events');
  var kinds = ['line','token','thinking','tool_call','tool_done','user','assistant','stats','status','error','busy','approval'];
  kinds.forEach(function (k) {
    es.addEventListener(k, function (m) {
      try { add(JSON.parse(m.data)); } catch (e) {}
    });
  });
  es.onopen = function () { dot.classList.add('on'); conn.textContent = 'connected'; };
  es.onerror = function () { dot.classList.remove('on'); conn.textContent = 'reconnecting…'; };

  function num(n) { return (n == null) ? '—' : String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ','); }
  function refreshState() {
    fetch('/api/state').then(function (r) { return r.json(); }).then(function (d) {
      var s = d.stats || {};
      document.getElementById('s-req').textContent = num(s.requests);
      document.getElementById('s-prompt').textContent = num(s.prompt_tk);
      document.getElementById('s-think').textContent = num(s.think_tk);
      document.getElementById('s-gen').textContent = num(s.gen_tk);
      document.getElementById('s-ttfb').textContent = s.last_ttfb_ms ? (s.last_ttfb_ms/1000).toFixed(1) + 's' : '—';
      updateComposer(!!d.can_submit);
      var ul = document.getElementById('todos');
      var td = d.todos || [];
      if (!td.length) { ul.innerHTML = '<li class="empty">none yet</li>'; return; }
      ul.innerHTML = td.map(function (t) {
        return '<li class="' + (t.done ? 'done' : '') + '"><span class="box">' +
          (t.done ? '[x]' : '[ ]') + '</span><span class="txt">' + escapeHtml(t.text) + '</span></li>';
      }).join('');
    }).catch(function () {});
  }
  refreshState();
  setInterval(refreshState, 2000);

  // Two-way: composer submits prompts when the server allows writes.
  var composer = document.getElementById('composer');
  var promptInput = document.getElementById('prompt');
  function updateComposer(canSubmit) {
    composer.style.display = canSubmit ? 'flex' : 'none';
  }
  composer.addEventListener('submit', function (e) {
    e.preventDefault();
    var text = promptInput.value.trim();
    if (!text) return;
    var btn = composer.querySelector('button');
    btn.disabled = true;
    fetch('/api/submit', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text: text })
    }).then(function (r) {
      if (r.ok) { promptInput.value = ''; }
      else { r.text().then(function (t) { add({ kind: 'error', text: 'submit failed: ' + t, time: '' }); }); }
    }).catch(function () {
      add({ kind: 'error', text: 'submit failed: network', time: '' });
    }).finally(function () { btn.disabled = false; });
  });
})();
</script>
</body>
</html>`
