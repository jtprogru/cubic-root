math.randomseed(os.time())

request = function()
  local random_value = math.random(1000000, 1000000000) -- Random number between 1kk and 1kkk
  local path = "http://localhost:8080/cubic-root" .. "?d=" .. random_value
  ---@diagnostic disable-next-line: undefined-global
  return wrk.format(nil, path)
end
