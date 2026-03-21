<p align="center">
  <h1 align="center">Soralink</h1>
  <p align="center">
    A self-hosted TCP tunnel and relay service that exposes your local server to the internet<br>
    without port forwarding or NAT configuration — just run it on your own VPS.<br>
    Open-source alternative to ngrok / playit.gg.
  </p>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> •
  <a href="#how-it-works">How It Works</a> •
  <a href="#installation">Installation</a> •
  <a href="#configuration">Configuration</a> •
  <a href="#roadmap">Roadmap</a> •
  <a href="#contributing">Contributing</a>
</p>

---

## Why Soralink?

|                 | ngrok / playit.gg             | Soralink                            |
| --------------- | ----------------------------- | ----------------------------------- |
| **Hosting**     | Third-party servers           | Your own VPS                        |
| **Cost**        | Free tier limits, paid plans  | Free (just VPS cost)                |
| **Control**     | Vendor-managed                | Full control over auth, ports, logs |
| **Privacy**     | Traffic passes through vendor | Traffic stays on your infra         |
| **Open Source** | Proprietary                   | ✅ MIT License                      |

## How It Works

```
                    Internet
                       │
                       ▼
┌──────────────────────────────────────────┐
│           Your VPS (soralink-server)     │
│                                          │
│   Control Port :4610  ◄── auth + tunnel  │
│   Tunnel Ports :10000-20000 ◄── data     │
└──────────────────────┬───────────────────┘
                       │ TCP relay
                       ▼
┌──────────────────────────────────────────┐
│        Your PC (soralink-client)         │
│                                          │
│        localhost:3000  ◄── your app      │
└──────────────────────────────────────────┘
```

1. **Client** connects to the **Server** over a control connection and authenticates
2. Server allocates a public port and starts listening for external traffic
3. When an external user connects, the server notifies the client
4. Client opens a data connection and bridges it to your local service
5. Traffic flows: `External User ↔ VPS ↔ Client ↔ localhost`

## Quick Start

### Prerequisites

- **Go 1.22+**
- A **VPS** with a public IP (Ubuntu 22.04+ recommended)
- Firewall: open ports **4610** and **10000-20000**

### 1. Build

```bash
git clone https://github.com/yuzu-krs/soralink.git
cd soralink

# Build both binaries
make build

# Or build for Linux (cross-compile for VPS deployment)
make build-linux
```

### 2. Configure the Server

Edit `configs/server.yaml`:

```yaml
control_port: 4610
auth_token: "your-secure-token-here" # ⚠️ Change this!
port_range:
  min: 10000
  max: 20000
log_level: "info"
```

Generate a secure token:

```bash
openssl rand -hex 32
```

### 3. Run the Server (on VPS)

```bash
./soralink-server --config configs/server.yaml
```

### 4. Configure the Client

Edit `configs/client.yaml`:

```yaml
server: "your-vps-ip:4610"
auth_token: "your-secure-token-here" # Must match server
tunnels:
  - local_port: 3000 # Your local service port
    remote_port: 0 # 0 = auto-assign
    protocol: "tcp"
log_level: "info"
```

### 5. Run the Client (on your PC)

```bash
# Config file mode
./soralink-client --config configs/client.yaml

# Or quick mode (CLI flags)
./soralink-client --server your-vps-ip:4610 --token your-token --local 3000
```

That's it! Your `localhost:3000` is now accessible at `your-vps-ip:<assigned-port>`.

## Installation

### From Source

```bash
go install github.com/yuzu-krs/soralink/cmd/soralink-server@latest
go install github.com/yuzu-krs/soralink/cmd/soralink-client@latest
```

### systemd (Production)

```bash
sudo cp deploy/soralink.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now soralink
```

See [deploy/soralink.service](deploy/soralink.service) for the service template.

## Configuration

### Server (`server.yaml`)

| Key              | Default | Description                                 |
| ---------------- | ------- | ------------------------------------------- |
| `control_port`   | `4610`  | Port for client control connections         |
| `auth_token`     | —       | Shared secret for authentication            |
| `port_range.min` | `10000` | Minimum tunnel port                         |
| `port_range.max` | `20000` | Maximum tunnel port                         |
| `log_level`      | `info`  | Log level: `debug`, `info`, `warn`, `error` |

### Client (`client.yaml`)

| Key                     | Default | Description                        |
| ----------------------- | ------- | ---------------------------------- |
| `server`                | —       | Server address (`host:port`)       |
| `auth_token`            | —       | Shared secret (must match server)  |
| `tunnels[].local_port`  | —       | Local port to expose               |
| `tunnels[].remote_port` | `0`     | Requested remote port (`0` = auto) |
| `tunnels[].protocol`    | `tcp`   | Protocol type                      |
| `log_level`             | `info`  | Log level                          |

## Project Structure

```
soralink/
├── cmd/
│   ├── soralink-server/    # Server entry point
│   └── soralink-client/    # Client entry point
├── internal/
│   ├── protocol/           # Wire protocol (frame encoding, messages)
│   ├── server/             # Server logic (tunnels, bridges, auth)
│   └── client/             # Client logic (connection, proxy)
├── configs/                # Example YAML configs
├── deploy/                 # systemd service file
└── doc/                    # Design documents
```

## Protocol

Soralink uses a custom binary frame protocol over TCP:

```
┌──────────┬─────────────┬─────────────────┐
│ Type (1B)│ Length (4B)  │ JSON Payload    │
└──────────┴─────────────┴─────────────────┘
```

| Type   | Name          | Direction       |
| ------ | ------------- | --------------- |
| `0x01` | Auth          | Client → Server |
| `0x02` | AuthResp      | Server → Client |
| `0x03` | RequestTunnel | Client → Server |
| `0x04` | TunnelReady   | Server → Client |
| `0x05` | NewConnection | Server → Client |
| `0x06` | Ping          | Both            |
| `0x07` | Pong          | Both            |
| `0x08` | Error         | Both            |
| `0x09` | CloseTunnel   | Client → Server |
| `0x0A` | DataConnInit  | Client → Server |

## Roadmap

- [x] TCP tunnel relay (MVP)
- [x] Token-based authentication
- [x] Multiple simultaneous tunnels
- [x] Custom binary protocol
- [ ] Ping/Pong health checks (server-initiated)
- [ ] Client auto-reconnect with exponential backoff
- [ ] Graceful shutdown
- [ ] TLS encryption
- [ ] HTTP tunneling with subdomain routing
- [ ] Rate limiting
- [ ] Web dashboard
- [ ] Let's Encrypt auto-certificates

See [doc/future-features.md](doc/future-features.md) for the full roadmap.

## Contributing

Contributions are welcome! This project is in active development.

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/your-feature`)
3. Commit your changes
4. Push to the branch and open a Pull Request

### Development

```bash
# Run tests
make test

# Build both binaries
make build

# Clean build artifacts
make clean
```

## License

This project is open source and available under the [MIT License](LICENSE).

---

<p align="center">
  <b>Soralink</b> — Expose your localhost to the world, on your own terms.
</p>
