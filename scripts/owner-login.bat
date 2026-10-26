@echo off
setlocal
cd /d "%~dp0.."
go run ./cmd/owner-login
exit /b %errorlevel%
