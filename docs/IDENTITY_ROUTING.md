# Connection identity, global authorization, and fallback

Bananasplit routes live connections; it does not make a source IP into an
identity. Every accepted flow receives an edge-generated `connection_id` and
may be bound to a short-lived join lease issued after browser authentication.

## Ownership boundaries

- Sessions owns global principals, WebAuthn devices, linked native-game
  identities, memberships, bans, and destination permissions in its durable
  control-plane database.
- Bananasplit owns short-lived join leases, connection-to-destination bindings,
  presence, handoff intent, and fallback decisions.
- PEEL owns sockets and edge connection IDs. It keeps only the minimum cached
  authorization needed by active connections.
- Protocol adapters may present or verify native identity, but cannot create
  permissions themselves.

The global authorization record is authoritative. Relays receive signed,
expiring decisions and never replicate password, cookie, passkey-private-key,
or payment data. A relay outage therefore loses sockets and expendable cache,
not identity or permission state.

## Join lease

`POST /join-leases` creates a one-use capability containing a principal,
browser device, requested destination, optional fallback destination, and an
expiry of at most five minutes. The raw token is returned once with
`Cache-Control: no-store`; only its SHA-256 digest is stored.

`POST /connections/resolve` consumes that capability and binds an immutable
edge connection ID to the selected live destination. If the requested
destination is absent, Bananasplit may use the explicitly authorized fallback.
The legacy IP route remains a compatibility path and is not an identity proof.

## Relay failure

A failed TCP relay cannot transfer an established socket to another process.
For an opaque TCP game, continuity means fast reconnect, not uninterrupted
transport. After reconnect:

1. the edge assigns a new connection ID;
2. globally stored identity and permissions remain valid;
3. Bananasplit resolves a fresh lease or a verified native identity;
4. unavailable destinations resolve to the authorized fallback lobby;
5. protocol-aware gateways may restore the intended backend or prompt the
   player to select another destination.

QUIC connection migration or a game-native resume token can provide stronger
continuity when both ends support it. PEEL must not claim transparent TCP
socket survival.

## Minecraft

Minecraft packet framing is adapter work. A Minecraft adapter may parse the
unencrypted handshake/status/login-start boundary to classify protocol and
route toward a trusted Velocity gateway. Once encryption begins, bulk bytes
remain in PEEL's native TCP bridge. Velocity owns authenticated UUIDs, backend
switching, and Minecraft transfer semantics; Paper remains a defense-in-depth
authorization boundary.

Packet splitting is bounded and opt-in. Adapters declare maximum frame,
buffer, CPU, and inspection limits. Unknown or encrypted traffic falls back to
opaque host-native forwarding rather than being copied packet-by-packet through
the policy runtime.
