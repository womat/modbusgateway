"""Capture the README screenshots and the social preview of the modbusgateway web UI.

Serves the real app/ui/index.html with a mocked /health, /devices and /activity (two buses, three
devices, REST and Modbus TCP clients and the client on the RTU line; new transactions arrive on
every request, so the LEDs flash and the rates are not zero) and photographs it with headless
Chromium. Run from the project root, on demand only:

    docker run --rm -v "$PWD":/src -w /src mcr.microsoft.com/playwright/python:v1.52.0-noble \
        sh -c 'pip install -q --break-system-packages playwright==1.52.0 && python3 docs/screenshots/capture.py'

Writes docs/screenshots/web-ui*.png and docs/social-preview.png.
"""

import base64
import json
import threading
import time
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from playwright.sync_api import sync_playwright

ROOT = Path(__file__).resolve().parents[2]
PAGE = (ROOT / "app/ui/index.html").read_bytes()
OUT = ROOT / "docs/screenshots"
PORT = 8767
UPTIME = 3 * 86400 + 4 * 3600 + 12 * 60
STEP = 0.4  # seconds between two sample transactions
START = time.time()

# Sample devices: name, bus, unit ID, functions, cache TTL, gateway listener and unit ID.
DEVICES = [
    ("energy-meter", "rs485", 1, ["FC3", "FC4"], 1, "tcp", 11),
    ("heatpump", "rs485", 2, ["FC1", "FC2", "FC3", "FC4", "FC5", "FC6", "FC15", "FC16"], 1, "tcp", 12),
    ("smartfox", "smartfox-lan", 1, ["FC3", "FC4"], 0, "rtu", 31),
]
BUSES = [
    ("rs485", "rtu", "/dev/ttyUSB0 9600 8N1", 0.016, 1),
    ("smartfox-lan", "tcp", "192.168.1.40:502", 0.004, 0),
]
BUS_OF = {d[0]: d[1] for d in DEVICES}

# The traffic repeats this cycle: source, client, device, unit ID on the listener, function code,
# address, quantity, result, class, duration in seconds.
CYCLE = [
    ("rest", "192.168.1.10", "energy-meter", 0, 3, 4096, 6, "ok", "ok", 0.015),
    ("tcp", "192.168.1.30", "energy-meter", 11, 3, 4096, 6, "cache", "cache", 0),
    ("rtu", "", "smartfox", 31, 3, 0, 10, "ok", "ok", 0.004),
    ("rest", "192.168.1.10", "heatpump", 0, 3, 4096, 8, "ok", "ok", 0.019),
    ("tcp", "192.168.1.31", "heatpump", 12, 1, 0, 8, "ok", "ok", 0.016),
    ("rest", "192.168.1.25", "smartfox", 0, 4, 0, 10, "ok", "ok", 0.005),
    ("tcp", "192.168.1.30", "energy-meter", 11, 3, 4096, 6, "ok", "ok", 0.015),
    ("rest", "192.168.1.10", "heatpump", 0, 6, 16, 1, "ok", "ok", 0.021),
    ("rtu", "", "smartfox", 31, 3, 0, 10, "ok", "ok", 0.003),
    ("tcp", "192.168.1.31", "energy-meter", 11, 3, 4096, 6, "cache", "cache", 0),
    ("rest", "192.168.1.25", "smartfox", 0, 6, 100, 1, "forbidden", "rejected", 0),
    ("tcp", "192.168.1.31", "heatpump", 12, 3, 4096, 8, "cache", "cache", 0),
]

# Clients: source, address, requests per minute, errors, targets as (device, unit ID), connected.
CLIENTS = [
    ("rest", "192.168.1.10", 46, 0, [("energy-meter", 0), ("heatpump", 0)], None),
    ("tcp", "192.168.1.30", 30, 0, [("energy-meter", 11)], True),
    ("tcp", "192.168.1.31", 46, 0, [("energy-meter", 11), ("heatpump", 12)], True),
    ("rtu", "", 30, 0, [("smartfox", 31)], None),
    ("rest", "192.168.1.25", 30, 14, [("smartfox", 0)], None),
]


def iso(t, ms=False):
    return datetime.fromtimestamp(t, timezone.utc).isoformat(timespec="milliseconds" if ms else "seconds").replace("+00:00", "Z")


def recent(now):
    seq = int((now - START) / STEP) + 1000
    out = []
    for k in range(100):
        s, client, device, unit, fc, addr, qty, result, cls, dur = CYCLE[(seq - k) % len(CYCLE)]
        out.append({"time": iso(START + (seq - 1000 - k) * STEP, ms=True), "source": s, "client": client,
                    "device": device, "unitId": unit, "functionCode": fc, "address": addr,
                    "quantity": qty, "result": result, "class": cls, "duration": dur})
    return out, seq


def health(now):
    return {"app": "modbusgateway", "appVersion": "2.0.0", "goVersion": "go1.27.1", "hostname": "modbus-pi",
            "os": "linux", "uptimeSeconds": UPTIME + now - START, "numGoroutines": 24,
            "heapAllocBytes": 3_400_000, "sysMemoryBytes": 12_600_000, "timestamp": iso(now)}


def devices(now):
    out = []
    for name, bus, unit, functions, ttl, listener, gw in DEVICES:
        hits, misses = (2840, 1160) if ttl else (0, 3990)
        out.append({"device": name, "bus": bus, "transport": dict((b[0], b[1]) for b in BUSES)[bus],
                    "unitId": unit, "functions": functions, "connected": True,
                    "lastConnectAt": iso(START - UPTIME), "lastSuccessAt": iso(now - 1),
                    "lastRequestAt": iso(now - 1), "queueLen": 0, "cacheHits": hits,
                    "cacheMisses": misses, "cacheTTL": ttl, "gateway": {"listener": listener, "unitId": gw}})
    return out


def activity(now):
    rec, seq = recent(now)
    clients = []
    for source, address, per_minute, errors, targets, connected in CLIENTS:
        c = {"source": source, "address": address, "firstAt": iso(now - 3600), "lastAt": iso(now - 1),
             "requests": per_minute * 60, "errors": errors * 4, "perMinute": per_minute,
             "targets": [{"device": d, "unitId": u} for d, u in targets]}
        if connected is not None:
            c["connected"] = connected
            c["connectedSince"] = iso(now - 2 * 3600)
        clients.append(c)
    buses = []
    for name, kind, address, mean, queue in BUSES:
        per_cycle = sum(1 for t in CYCLE if BUS_OF[t[2]] == name and t[8] == "ok")
        buses.append({"name": name, "type": kind, "address": address, "connected": True,
                      "lastConnectAt": iso(START - UPTIME), "queueLen": queue, "queueSize": 32,
                      "transactions": 180_000 + seq * per_cycle // len(CYCLE), "errors": 3, "timeouts": 1,
                      "meanDuration": mean})
    return {"listeners": [
        {"source": "rest", "address": "[::]:8443"},
        {"source": "tcp", "address": "[::]:502", "openConnections": 2},
        {"source": "rtu", "address": "/dev/ttyUSB1", "settings": "9600 8N1"},
    ], "clients": clients, "buses": buses, "recent": rec}


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        now = time.time()
        if self.path == "/":
            body, ctype = PAGE, "text/html; charset=utf-8"
        elif self.path in ("/health", "/devices", "/activity"):
            body = json.dumps({"/health": health, "/devices": devices, "/activity": activity}[self.path](now)).encode()
            ctype = "application/json"
        else:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


def shoot(browser, path, width, height, scheme, scale=1, full_page=True):
    ctx = browser.new_context(viewport={"width": width, "height": height},
                              color_scheme=scheme, device_scale_factor=scale)
    ctx.add_init_script("localStorage.setItem('modbusgateway.apiKey', 'demo')")
    page = ctx.new_page()
    page.goto(f"http://localhost:{PORT}/")
    # The second poll brings the rates and the first flashes; catch a moment with a lit device LED.
    page.wait_for_function("document.querySelector('.tap .stub .dot.on') !== null", polling="raf", timeout=10000)
    page.screenshot(path=str(path), full_page=full_page)
    ctx.close()
    print("wrote", path.relative_to(ROOT))


def social(browser):
    shot = base64.b64encode((OUT / "web-ui.png").read_bytes()).decode()
    html = f"""<!doctype html><meta charset="utf-8">
<style>
  body {{ margin: 0; width: 1280px; height: 640px; background: #f3f5f7; font-family: system-ui, sans-serif;
         display: flex; align-items: center; gap: 56px; padding: 0 0 0 96px; box-sizing: border-box; overflow: hidden; }}
  .text {{ display: flex; flex-direction: column; gap: 22px; width: 470px; flex: none; }}
  .brand {{ display: flex; align-items: center; gap: 20px; }}
  svg {{ width: 96px; height: 72px; color: #2563a8; flex: none; }}
  h1 {{ margin: 0; font-size: 60px; letter-spacing: -.02em; color: #17202b; line-height: 1; }}
  h1 b {{ color: #2563a8; display: block; }}
  p {{ margin: 0; font-size: 30px; line-height: 1.3; color: #3d4a58; }}
  .tags {{ font-size: 21px; color: #5d6b7a; }}
  img {{ height: 520px; border-radius: 14px; box-shadow: 0 20px 50px rgba(23, 32, 43, .18);
         border: 1px solid #dde2e8; object-fit: cover; object-position: left top; width: 900px; }}
</style>
<div class="text">
  <div class="brand"><svg viewBox="0 0 40 30" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round">
    <path d="M2 8h8M2 15h8M2 22h8"/><rect x="12" y="4" width="14" height="22" rx="3"/><path d="M28 15h10"/>
    <circle cx="19" cy="10" r="1.8" fill="#22c55e" stroke="none"/></svg>
    <h1>modbus<b>gateway</b></h1></div>
  <p>Your Modbus devices on the network, from a Raspberry Pi.</p>
  <span class="tags">REST API · Modbus TCP and RTU · live web page</span>
</div>
<img src="data:image/png;base64,{shot}" alt="">"""
    page = browser.new_page(viewport={"width": 1280, "height": 640})
    page.set_content(html)
    path = ROOT / "docs/social-preview.png"
    page.screenshot(path=str(path))
    page.close()
    print("wrote", path.relative_to(ROOT))


def main():
    server = ThreadingHTTPServer(("localhost", PORT), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    OUT.mkdir(parents=True, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch()
        shoot(browser, OUT / "web-ui.png", 1280, 900, "light")
        shoot(browser, OUT / "web-ui-dark.png", 1280, 900, "dark")
        # Phone: the first screen only.
        shoot(browser, OUT / "web-ui-phone.png", 390, 760, "light", scale=2, full_page=False)
        social(browser)
        browser.close()
    server.shutdown()


if __name__ == "__main__":
    main()
