@echo off
rem Builds altesp for every supported platform into one bin\<yyyy-mm-dd-HH-mm>-<version>\.
setlocal
for /f %%t in ('powershell -NoProfile -Command "Get-Date -Format yyyy-MM-dd-HH-mm"') do set "BUILD_STAMP=%%t"
for %%t in (windows:amd64 linux:amd64 linux:arm64 darwin:arm64) do (
  for /f "tokens=1,2 delims=:" %%a in ("%%t") do (
    call "%~dp0_build.bat" %%a %%b || exit /b 1
  )
)
