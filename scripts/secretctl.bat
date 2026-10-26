@echo off
setlocal
pushd "%~dp0.."
go run ./cmd/secretctl %*
set "RESULT=%ERRORLEVEL%"
popd
exit /b %RESULT%
