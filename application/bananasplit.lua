local M = {}

local function call(cell, provider, payload)
  return pulp.unpack(pulp.call_raw(cell, provider, pulp.pack(payload)))
end

local function owner(provider, payload)
  return call("coordination-state", provider, payload)
end

local function http(method, url, body, headers)
  return call("http-json", "engine.http-json.v1.request", {
    method = method, url = url, body = body, headers = headers or {}, timeout_ms = 5000,
  })
end

local function registry_headers()
  local headers = { ["Content-Type"] = "application/json" }
  if pulp.config.bananagine_token ~= nil and pulp.config.bananagine_token ~= "" then
    headers["X-Service-Token"] = pulp.config.bananagine_token
  end
  return headers
end

local function registry_get(path)
  local response = http("GET", pulp.config.bananagine_url .. path, nil, registry_headers())
  if response.status < 200 or response.status >= 300 then error("registry request failed") end
  return response.value
end

local function backend(server)
  return server.host .. ":" .. tostring(server.port)
end

local function safe_webhook(server)
  local host = server.host
  local port = tonumber(server.webhookPort)
  if host == nil or host == "" or port == nil or port <= 0 or port > 65535 then return false end
  local lowered = string.lower(host)
  if lowered == "localhost" or lowered == "::1" or lowered == "0.0.0.0" or
      string.match(lowered, "^127%.") or string.match(lowered, "^169%.254%.") or
      string.match(lowered, "^fe80:") then return false end
  return true
end

local function effect(provider, payload)
  if provider == "bananasplit.effects.v1.lobby.find-capacity" then
    for _, server in ipairs(registry_get("/registry/servers?type=lobby") or {}) do
      if server.maxPlayers == nil or server.maxPlayers == 0 or server.players < server.maxPlayers then
        return { found = true, server = server, backend = backend(server) }
      end
    end
    return { found = false }
  end
  if provider == "bananasplit.effects.v1.lobby.find" then
    local servers = registry_get("/registry/servers?type=lobby&hasCapacity=true") or {}
    if #servers == 0 then return { found = false } end
    return { found = true, server = servers[1], backend = backend(servers[1]) }
  end
  if provider == "bananasplit.effects.v1.match.find-ready" then
    for _, server in ipairs(registry_get("/registry/servers?type=game&mode=" .. payload.mode .. "&hasReadyMatch=true") or {}) do
      for match_id, match in pairs(server.matches or {}) do
        if match.status == "ready" then
          return { found = true, server = server, match_id = match_id, need = match.need }
        end
      end
    end
    return { found = false }
  end
  if provider == "bananasplit.effects.v1.match.expect" then
    if not safe_webhook(payload.server) then return { ok = false } end
    local uuids = {}
    for _, player in ipairs(payload.players or {}) do table.insert(uuids, player.uuid) end
    local response = http("POST", "http://" .. payload.server.host .. ":" .. tostring(payload.server.webhookPort) .. "/expect",
      { matchId = payload.match_id, uuids = uuids })
    return { ok = response.status >= 200 and response.status < 300 }
  end
  if provider == "bananasplit.effects.v1.match.status" then
    local uuids = {}
    for _, player in ipairs(payload.players or {}) do table.insert(uuids, player.uuid) end
    local response = http("PUT", pulp.config.bananagine_url .. "/registry/servers/" .. payload.server_id .. "/matches/" .. payload.match_id,
      { status = "busy", need = #uuids, players = uuids }, registry_headers())
    return { ok = response.status >= 200 and response.status < 300 }
  end
  if provider == "bananasplit.effects.v1.lobbies.notify" then
    local grouped = {}
    for _, player in ipairs(payload.players or {}) do
      grouped[player.lobby_server] = grouped[player.lobby_server] or {}
      table.insert(grouped[player.lobby_server], player.uuid)
    end
    for lobby_id, players in pairs(grouped) do
      local lobby = registry_get("/registry/servers/" .. lobby_id)
      if lobby ~= nil and safe_webhook(lobby) then
        pcall(function()
          http("POST", "http://" .. lobby.host .. ":" .. tostring(lobby.webhookPort) .. "/match", {
            matchId = payload.match_id, mode = payload.mode, players = players, gameServer = backend(payload.server),
          })
        end)
      end
    end
    return { ok = true }
  end
  if provider == "bananasplit.effects.v1.peel.route-set" then
    if pulp.config.peel_url == nil or pulp.config.peel_url == "" then return { ok = true } end
    local response = http("POST", pulp.config.peel_url .. "/routes", {
      player_ip = payload.player_ip, backend = payload.backend,
    })
    if response.status < 200 or response.status >= 300 then error("route set failed") end
    return { ok = true }
  end
  if provider == "bananasplit.effects.v1.peel.route-delete" then
    if pulp.config.peel_url == nil or pulp.config.peel_url == "" then return { ok = true } end
    local response = http("DELETE", pulp.config.peel_url .. "/routes/" .. payload.player_ip)
    if response.status ~= 200 and response.status ~= 204 then error("route delete failed") end
    return { ok = true }
  end
  error("unsupported BananaSplit effect " .. provider)
end

function M.health(_)
  return { status = "ok" }
end

function M.queue_join(payload)
  local result = owner("coordination.v1.queue.join", {
    id = payload.id,
    queue = payload.mode,
    member = {
      participant_id = payload.uuid,
      origin = payload.lobby_server,
      joined_at = payload.joined_at,
    },
  })
  return { status = result.status, mode = result.queue, position = result.position }
end

function M.queue_leave(payload)
  return owner("coordination.v1.queue.leave", {
    id = payload.id, queue = payload.mode, participant_id = payload.uuid,
  })
end

function M.queue_size(payload)
  local result = owner("coordination.v1.queue.size", { queue = payload.mode })
  return { mode = result.queue, size = result.size }
end

function M.route_request(payload)
  local lobby = effect("bananasplit.effects.v1.lobby.find-capacity", {})
  if lobby.found ~= true then
    return { http_status = 503, error = "no lobbies available" }
  end
  owner("coordination.v1.directory.put", {
    id = payload.id .. ":binding",
    key = payload.player_ip,
    record = {
      subject_id = payload.player_ip,
      address = payload.player_ip,
      placement = lobby.server.id,
    },
  })
  pcall(function()
    effect("bananasplit.effects.v1.peel.route-set", {
      player_ip = payload.player_ip,
      backend = lobby.backend,
    })
  end)
  return { backend = lobby.backend, server_id = lobby.server.id }
end

function M.assign(_)
  local lobby = effect("bananasplit.effects.v1.lobby.find", {})
  if lobby.found ~= true then
    return { http_status = 503, error = "no lobby available" }
  end
  return { backend = lobby.backend }
end

function M.player_register(payload)
  owner("coordination.v1.directory.put", {
    id = payload.id,
    key = payload.player_uuid,
    record = {
      subject_id = payload.player_uuid,
      address = payload.player_ip,
      placement = payload.server_id,
    },
  })
  return { status = "ok" }
end

function M.player_remove(payload)
  local removed = owner("coordination.v1.directory.remove", { id = payload.id, key = payload.uuid })
  if removed.found == true and removed.record ~= nil then
    pcall(function()
      effect("bananasplit.effects.v1.peel.route-delete", {
        player_ip = removed.record.address,
      })
    end)
  end
  return { status = "ok" }
end

function M.referrals(payload)
  local handoffs = owner("coordination.v1.mailbox.take", { id = payload.id, mailbox = payload.server_id }).items
  local referrals = {}
  for _, item in ipairs(handoffs or {}) do
    table.insert(referrals, { player_uuid = item.subject_id, host = item.host, port = item.port })
  end
  return { items = referrals, empty = #referrals == 0 }
end

function M.match_complete(payload)
  local lobby = effect("bananasplit.effects.v1.lobby.find", {})
  for index, player in ipairs(payload.players or {}) do
    if player.action ~= "requeue" and lobby.found == true then
      local stored = owner("coordination.v1.directory.get", { key = player.uuid })
      if stored.found == true and stored.record ~= nil then
        pcall(function()
          effect("bananasplit.effects.v1.peel.route-set", {
            player_ip = stored.record.address,
            backend = lobby.backend,
          })
        end)
        owner("coordination.v1.mailbox.append", {
          id = payload.id .. ":referral:" .. tostring(index) .. ":" .. player.uuid,
          mailbox = payload.server_id,
          item = {
            subject_id = player.uuid,
            host = payload.relay_host,
            port = payload.relay_port,
          },
        })
      end
    end
  end
  return { status = "processed" }
end

local function match_mode(payload, mode)
  local ready = effect("bananasplit.effects.v1.match.find-ready", { mode = mode })
  if ready.found ~= true then return end
  local reservation = owner("coordination.v1.group.reserve", {
    id = "reserve:" .. payload.wall_nanos .. ":" .. mode .. ":" .. ready.server.id .. ":" .. ready.match_id,
    queue = mode,
    allocation_id = ready.match_id,
    target_id = ready.server.id,
    size = ready.need,
    created_at = payload.now,
  })
  if reservation.reserved ~= true or reservation.reservation == nil then return end

  local players = {}
  for _, member in ipairs(reservation.reservation.members or {}) do
    table.insert(players, { uuid = member.participant_id, lobby_server = member.origin, joined_at = member.joined_at })
  end
  local expected = effect("bananasplit.effects.v1.match.expect", {
    server = ready.server,
    match_id = ready.match_id,
    players = players,
  })
  if expected.ok ~= true then
    owner("coordination.v1.group.release", {
      id = reservation.reservation.id .. ":fail",
      reservation_id = reservation.reservation.id,
      settled_at = payload.now,
    })
    return
  end

  pcall(function()
    effect("bananasplit.effects.v1.match.status", {
      server_id = ready.server.id,
      match_id = ready.match_id,
      players = players,
    })
  end)
  pcall(function()
    effect("bananasplit.effects.v1.lobbies.notify", {
      server = ready.server,
      match_id = ready.match_id,
      mode = mode,
      players = players,
    })
  end)
  owner("coordination.v1.group.commit", {
    id = reservation.reservation.id .. ":commit",
    reservation_id = reservation.reservation.id,
    settled_at = payload.now,
  })
end

function M.tick(payload)
  local due = owner("coordination.v1.schedule.due", {
    id = "tick:" .. payload.wall_nanos,
    wall_nanos = payload.wall_nanos,
    tick_nanos = payload.tick_nanos,
    cleanup_every_nanos = payload.cleanup_every_nanos,
  })
  if due.maintenance == true then
    owner("coordination.v1.queue.expire", {
      id = "cleanup:" .. payload.wall_nanos,
      now = payload.now,
      timeout_seconds = payload.queue_timeout_seconds,
    })
  end
  if due.ready == true then
    local modes = owner("coordination.v1.queue.list", {}).queues
    for _, mode in ipairs(modes or {}) do
      match_mode(payload, mode)
    end
  end
  return due
end

local EVENTS = {
  ["bananasplit.http.health.v1"] = M.health,
  ["bananasplit.http.queue.join.v1"] = M.queue_join,
  ["bananasplit.http.queue.leave.v1"] = M.queue_leave,
  ["bananasplit.http.queue.size.v1"] = M.queue_size,
  ["bananasplit.http.route-request.v1"] = M.route_request,
  ["bananasplit.http.assign.v1"] = M.assign,
  ["bananasplit.http.player.register.v1"] = M.player_register,
  ["bananasplit.http.player.remove.v1"] = M.player_remove,
  ["bananasplit.http.referrals.v1"] = M.referrals,
  ["bananasplit.http.match-complete.v1"] = M.match_complete,
  ["bananasplit.tick.v1"] = M.tick,
}

for event, handler in pairs(EVENTS) do pulp.on(event, handler) end

M.events = EVENTS
return M
