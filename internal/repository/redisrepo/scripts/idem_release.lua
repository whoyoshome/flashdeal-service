-- idem_release.lua drops an in-progress claim only if it is still the one
-- this request made, so a slow request can never delete a newer claim or a
-- stored final response.
--
-- KEYS[1] = idem:{<user_id>}:<key>   ARGV[1] = claim token
local current = redis.call('GET', KEYS[1])
if not current then
  return 0
end
local record = cjson.decode(current)
if record.state == 'in_progress' and record.token == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
