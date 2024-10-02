math.randomseed(os.time())
request = function()
    local random_value = math.random(1, 1000)  -- Random number between 1 and 1000
    local path = "http://localhost:8080/cubic-root" .. "?d=" .. random_value
    return wrk.format(nil, path)
end
