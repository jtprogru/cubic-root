#!/bin/sh
set -e

# Устанавливаем значение по умолчанию для переменной DEBUG, если она не задана
if [ -z "$DEBUG" ]; then
  export DEBUG=false
fi

# Выводим сообщение о старте приложения, если DEBUG=true
if [ "$DEBUG" = "true" ]; then
  echo "Debug mode is enabled"
fi

# Запускаем приложение
exec "$@"
