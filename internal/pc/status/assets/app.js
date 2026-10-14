"use strict";

// The page keeps the ?token= it was opened with and sends it along with API calls.
const token = new URLSearchParams(location.search).get("token");
const query = token ? "?token=" + encodeURIComponent(token) : "";

const $ = (id) => document.getElementById(id);

function bytes(n) {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return (i === 0 ? n : n.toFixed(1)) + " " + units[i];
}

function when(iso) {
  const d = new Date(iso);
  return isNaN(d) || d.getFullYear() < 2000 ? "-" : d.toLocaleString();
}

function fill(tbody, rows) {
  tbody.replaceChildren(...rows.map((cells) => {
    const tr = document.createElement("tr");
    for (const text of cells) {
      const td = document.createElement("td");
      td.textContent = text;
      tr.append(td);
    }
    return tr;
  }));
}

function render(s) {
  $("version").textContent = s.Version || "";
  $("state").textContent = s.State || "unknown";
  $("network").textContent = s.Network || "unknown network";
  $("last-check").textContent = when(s.LastCheck);
  $("next-check").textContent = when(s.NextCheck);
  $("savings").textContent = Math.round((s.Savings || 0) * 100) + "%";

  const routes = Object.keys(Object.assign({}, s.Routes, s.Traffic)).sort();
  fill($("routes").tBodies[0], routes.map((r) => [r, String((s.Routes || {})[r] || 0), bytes((s.Traffic || {})[r] || 0)]));
  fill($("processes").tBodies[0], (s.Processes || []).map((p) => [p.Name, p.State, String(p.Restarts)]));
  fill($("recent").tBodies[0], (s.Recent || []).map((c) => [when(c.At), c.Domain, c.From || "-", c.To || "-"]));

  const warnings = $("warnings");
  warnings.replaceChildren(...(s.Warnings || []).map((w) => {
    const li = document.createElement("li");
    li.textContent = w;
    return li;
  }));
}

async function refresh() {
  try {
    const res = await fetch("/api/status" + query, { cache: "no-store" });
    if (!res.ok) { throw new Error(String(res.status)); }
    render(await res.json());
    $("error").hidden = true;
  } catch (e) {
    $("error").hidden = false;
  }
}

refresh();
setInterval(refresh, 3000);
