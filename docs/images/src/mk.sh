#!/bin/zsh
# mk.sh <name> <cssWidth> <bodyFile>  -> builds <name>.html
name=$1; w=$2; body=$3
{
  echo '<!doctype html><html lang="en"><head><meta charset="utf-8" /><style>'
  cat demo-shared.css
  echo "html,body{width:${w}px}"
  echo '</style></head><body>'
  cat "$body"
  echo '</body></html>'
} > "$name.html"
echo "built $name.html"
