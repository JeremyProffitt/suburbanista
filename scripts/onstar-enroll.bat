@echo off
setlocal
pushd "%~dp0.."
go run ./cmd/onstar-enroll --attended %*
set "RESULT=%ERRORLEVEL%"
popd
exit /b %RESULT%
