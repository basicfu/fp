@echo off
rem Windows associates .sh with git-bash.exe, which opens a separate mintty
rem window that closes as soon as the script exits (errors flash and vanish).
rem Run the same-named .sh with Git's console bash.exe in the current terminal
rem instead. Located via git rather than PATH: a bare "bash" may resolve to WSL.
setlocal
for /f "delims=" %%E in ('git --exec-path 2^>nul') do set "GIT_EXEC=%%E"
if not defined GIT_EXEC (
  echo git not found in PATH 1>&2
  exit /b 1
)
for %%R in ("%GIT_EXEC%\..\..\..") do set "GIT_ROOT=%%~fR"
"%GIT_ROOT%\bin\bash.exe" "%~dpn0.sh" %*
exit /b %ERRORLEVEL%
