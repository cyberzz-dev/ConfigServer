@echo off
rem build-linux.bat — Cross-compile ConfigServer for Linux (Windows wrapper)
rem Usage:
rem   build-linux.bat                             build all binaries for linux/amd64
rem   build-linux.bat amd64                       build all binaries for linux/amd64
rem   build-linux.bat arm64                       build all binaries for linux/arm64
rem   build-linux.bat amd64 -Target allinone      build only allinone
rem   build-linux.bat amd64 -SkipWebUI            skip npm build
rem   build-linux.bat amd64 -Version 1.0.0        embed version string

powershell -ExecutionPolicy Bypass -File "%~dp0build-linux.ps1" %*
