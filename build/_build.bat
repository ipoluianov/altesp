@echo off
rem Builds altesp for one platform into bin\<yyyy-mm-dd-HH-mm>-<version>\.
rem Usage: build\_build.bat <goos> <goarch>
rem BUILD_STAMP (set by all.bat) puts several platforms into one directory.
setlocal

set "T_OS=%~1"
set "T_ARCH=%~2"
set "APP=altesp"
set "DISPLAY_NAME=AltEsp"
set "REPO=ipoluianov/altesp"
set "NFPM_VERSION=v2.47.0"
set "WINRES_VERSION=v0.3.3"
pushd "%~dp0.."

set "VERSION=dev"
for /f "delims=" %%v in ('git describe --tags --always --dirty 2^>nul') do set "VERSION=%%v"
rem The numbers of the version for the exe: v1.2.3-4-gabc -> 1.2.3
for /f "tokens=1 delims=-" %%v in ("%VERSION%") do set "NUM_VERSION=%%v"
if "%NUM_VERSION:~0,1%"=="v" set "NUM_VERSION=%NUM_VERSION:~1%"
echo %NUM_VERSION%| findstr /r "^[0-9][0-9.]*$" >nul || set "NUM_VERSION=0.0.0"

set "LDFLAGS=-s -w -X github.com/ipoluianov/altesp/app.Version=%VERSION%"
set "EXT="
if "%T_OS%"=="windows" (
  set "EXT=.exe"
  set "LDFLAGS=%LDFLAGS% -H=windowsgui"
)

rem %date% depends on the locale, so the time comes from PowerShell
set "STAMP=%BUILD_STAMP%"
if not defined STAMP for /f %%t in ('powershell -NoProfile -Command "Get-Date -Format yyyy-MM-dd-HH-mm"') do set "STAMP=%%t"
set "DIR=bin\%STAMP%-%VERSION%"
set "OUT=%DIR%\%APP%-%T_OS%-%T_ARCH%%EXT%"
rem Windows is built for amd64 only, so its exe needs no platform suffix
if "%T_OS%"=="windows" set "OUT=%DIR%\%APP%.exe"
if not exist "%DIR%" mkdir "%DIR%"
echo Building %OUT%
if not "%T_OS%"=="windows" goto build
rem The icon and the version shown by Explorer; go build links the .syso in
go run "github.com/tc-hib/go-winres@%WINRES_VERSION%" simply --arch %T_ARCH% --out rsrc --manifest none --icon icon.png --product-name %DISPLAY_NAME% --file-description %DISPLAY_NAME% --original-filename %APP%.exe --copyright "Ivan Poluianov" --file-version %NUM_VERSION% --product-version %VERSION%
if errorlevel 1 (
  set "RC=1"
  goto done
)

:build
set "CGO_ENABLED=0"
set "GOOS=%T_OS%"
set "GOARCH=%T_ARCH%"
go build -trimpath -ldflags="%LDFLAGS%" -o "%OUT%" .
set "RC=%ERRORLEVEL%"
del /q rsrc_windows_*.syso 2>nul
rem The tools below (go run nfpm) are built for this machine
set "GOOS="
set "GOARCH="
set "CGO_ENABLED="
if not "%RC%"=="0" goto done

if "%T_OS%"=="linux" goto linux
if "%T_OS%"=="darwin" goto darwin
goto done

:linux
rem nfpm is pure Go, so the packages are built on any system
if not exist bin\.pkg mkdir bin\.pkg
copy /y "%OUT%" bin\.pkg\%APP% >nul
set "PKG_ARCH=%T_ARCH%"
set "PKG_VERSION=%VERSION%"
for %%f in (deb rpm) do (
  go run "github.com/goreleaser/nfpm/v2/cmd/nfpm@%NFPM_VERSION%" pkg --config build/nfpm.yaml --packager %%f --target "%OUT%.%%f" || set "RC=1"
)

rem The archive for scripts\linux-install.sh: the binary and the menu icon
copy /y icon.svg bin\.pkg\%APP%.svg >nul
set "TAR=tar"
if exist "%SystemRoot%\System32\tar.exe" set "TAR=%SystemRoot%\System32\tar.exe"
"%TAR%" -czf "%DIR%\%APP%-%VERSION%-linux-%T_ARCH%.tar.gz" -C bin\.pkg %APP% %APP%.svg || set "RC=1"
rmdir /s /q bin\.pkg

rem The installer downloads the archive for its machine from the release of
rem this tag; it is the same for both architectures, so each build writes it.
rem Written as UTF-8 without BOM and with LF, or bash would not run it.
powershell -NoProfile -NonInteractive -Command "$s = [IO.File]::ReadAllText('scripts\linux-install.sh') -replace \"`r\", '' -replace '__APP__', '%APP%' -replace '__DISPLAY_NAME__', '%DISPLAY_NAME%' -replace '__TAG__', '%VERSION%' -replace '__REPO__', '%REPO%'; [IO.File]::WriteAllText('%DIR%\linux-install.sh', $s, (New-Object Text.UTF8Encoding $false))" || set "RC=1"
echo %VERSION%| findstr /r "^v[0-9.]*$" >nul || echo Warning: %VERSION% is not a clean tag, linux-install.sh points to a release that may not exist
goto done

:darwin
rem The .dmg is packed by _dmg.sh, so it needs Git Bash
rem (bash.exe of System32 is the launcher of WSL, not used here)
set "GITBASH="
for /f "delims=" %%g in ('where git 2^>nul') do (
  if not defined GITBASH if exist "%%~dpg..\bin\bash.exe" set "GITBASH=%%~dpg..\bin\bash.exe"
)
if not defined GITBASH if exist "%ProgramFiles%\Git\bin\bash.exe" set "GITBASH=%ProgramFiles%\Git\bin\bash.exe"
if not defined GITBASH (
  echo Warning: Git Bash not found, skipping %OUT%.dmg
  goto done
)
"%GITBASH%" build/_dmg.sh "%OUT:\=/%" "%VERSION%"
set "RC=%ERRORLEVEL%"

:done
popd
exit /b %RC%
