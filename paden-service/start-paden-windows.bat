@echo off
setlocal enabledelayedexpansion
rem Use UTF-8 if available, fallback to default
chcp 65001 >nul 2>&1
cd /d "%~dp0"

echo ============================================
echo   paden-service build and start
echo ============================================
echo.

rem ============================================================
rem Find suitable Python (3.10+)
rem ============================================================
set "PYTHON_CMD="

rem 1. Try python3 if available (some systems have python3 separately)
where python3 >nul 2>&1
if %errorlevel% equ 0 (
    for /f "delims=" %%i in ('python3 --version 2^>^&1') do set PY_VER=%%i
    echo Found: python3 !PY_VER!
    set "PYTHON_CMD=python3"
    goto :check_version
)

rem 2. Try py launcher (recommended on Windows)
where py >nul 2>&1
if %errorlevel% equ 0 (
    rem Try to find Python 3.10+
    for /f "delims=" %%i in ('py -3.10 --version 2^>nul') do set PY_VER=%%i
    if not "!PY_VER!"=="" (
        echo Found: py -3.10 !PY_VER!
        set "PYTHON_CMD=py -3.10"
        goto :start_service
    )
    for /f "delims=" %%i in ('py -3 --version 2^>nul') do set PY_VER=%%i
    if not "!PY_VER!"=="" (
        echo Found: py -3 !PY_VER!
        set "PYTHON_CMD=py -3"
        goto :check_version
    )
    rem Fallback to default Python from launcher
    for /f "delims=" %%i in ('py --version 2^>nul') do set PY_VER=%%i
    if not "!PY_VER!"=="" (
        echo Found: py !PY_VER!
        set "PYTHON_CMD=py"
        goto :check_version
    )
)

rem 3. Try python command
where python >nul 2>&1
if %errorlevel% equ 0 (
    for /f "delims=" %%i in ('python --version 2^>^&1') do set PY_VER=%%i
    echo Found: python !PY_VER!
    set "PYTHON_CMD=python"
    goto :check_version
)

rem No Python found
echo [ERROR] Python not found
echo.
echo Please install Python 3.10 or higher:
echo   https://www.python.org/downloads/
echo.
echo During installation, check "Add Python to PATH".
pause
exit /b 1

:check_version
rem Extract version number (handle "Python 3.x.x" format)
for /f "tokens=2" %%a in ("!PY_VER!") do set PY_NUM=%%a
for /f "tokens=1,2 delims=." %%x in ("!PY_NUM!") do (
    set MAJOR=%%x
    set MINOR=%%y
)

rem Check if version is at least 3.10
if !MAJOR! LSS 3 (
    echo [ERROR] Python version too old: !PY_VER!
    echo Required: Python 3.10 or higher
    pause
exit /b 1
)
if !MAJOR! EQU 3 if !MINOR! LSS 10 (
    echo [ERROR] Python version too old: !PY_VER!
    echo Required: Python 3.10 or higher
    pause
    exit /b 1
)

echo [OK] Python !PY_VER! (meets 3.10+ requirement)
echo.

:start_service
rem ============================================================
rem Check local files
rem ============================================================
echo [1/4] Checking local files...

if not exist "main.py" (
    echo [ERROR] main.py not found
    echo.
    echo Please make sure this script is placed inside the paden-service directory:
    echo   paden-service\start-paden-windows.bat    ^<- here
    echo   paden-service\main.py
    pause
    exit /b 1
)

if not exist "problem.py" (
    echo [ERROR] problem.py not found
    pause
    exit /b 1
)

if not exist "solver.py" (
    if not exist "solver_enhanced.py" (
        echo [ERROR] Neither solver.py nor solver_enhanced.py found
        pause
        exit /b 1
    )
)

echo       All required files present
echo.

rem ============================================================
rem Check and install dependencies
rem ============================================================
echo [2/4] Checking dependencies...
set MISSING=0

rem Check standard packages
for %%P in (numpy scipy shapely fastapi uvicorn pydantic matplotlib trimesh) do (
    !PYTHON_CMD! -c "import %%P" >nul 2>&1
    if errorlevel 1 (
        set MISSING=1
        goto :install_deps
    )
)

rem Check pygerber (both old and new API)
!PYTHON_CMD! -c "from pygerber.gerberx3.api.v2 import GerberFile" >nul 2>&1
if errorlevel 1 (
    !PYTHON_CMD! -c "import pygerber.gerber.api" >nul 2>&1
    if errorlevel 1 (
        set MISSING=1
        goto :install_deps
    )
)

echo       All dependencies installed
goto :syntax_check

:install_deps
echo       Installing missing packages...
echo.

rem Try official PyPI first
echo [1/3] Trying official PyPI...
!PYTHON_CMD! -m pip install numpy scipy shapely fastapi uvicorn pydantic matplotlib trimesh "pygerber>=3.0.0a3" --pre --quiet --upgrade
if %errorlevel% equ 0 (
    echo       Installation successful!
    goto :syntax_check
)

rem Try Tsinghua mirror (China)
echo [2/3] Official PyPI failed, trying Tsinghua mirror...
!PYTHON_CMD! -m pip install numpy scipy shapely fastapi uvicorn pydantic matplotlib trimesh "pygerber>=3.0.0a3" --pre --quiet --upgrade -i https://pypi.tuna.tsinghua.edu.cn/simple
if %errorlevel% equ 0 (
    echo       Installation successful!
    goto :syntax_check
)

rem Try Aliyun mirror (China)
echo [3/3] Trying Aliyun mirror...
!PYTHON_CMD! -m pip install numpy scipy shapely fastapi uvicorn pydantic matplotlib trimesh "pygerber>=3.0.0a3" --pre --quiet --upgrade -i https://mirrors.aliyun.com/pypi/simple/
if %errorlevel% equ 0 (
    echo       Installation successful!
    goto :syntax_check
)

rem All attempts failed
echo [ERROR] All installation attempts failed
echo.
echo Please try manually:
echo   pip install numpy scipy shapely fastapi uvicorn pydantic matplotlib trimesh
echo   pip install "pygerber>=3.0.0a3" --pre
echo.
echo Or use a mirror:
echo   pip install -i https://pypi.tuna.tsinghua.edu.cn/simple "pygerber>=3.0.0a3" --pre
pause
exit /b 1

:syntax_check
rem ============================================================
rem Syntax check
rem ============================================================
echo.
echo [3/4] Syntax check...

!PYTHON_CMD! -c "import main" 2>&1
if %errorlevel% neq 0 (
    echo [ERROR] Syntax check failed on main.py
    pause
    exit /b 1
)

if exist "solver.py" (
    !PYTHON_CMD! -m py_compile solver.py >nul 2>&1
    if %errorlevel% neq 0 (
        echo [ERROR] Syntax check failed on solver.py
        pause
        exit /b 1
    )
)

if exist "solver_enhanced.py" (
    !PYTHON_CMD! -c "import solver_enhanced" 2>&1
    if %errorlevel% neq 0 (
        echo [ERROR] Syntax check failed on solver_enhanced.py
        pause
        exit /b 1
    )
)

!PYTHON_CMD! -m py_compile problem.py >nul 2>&1
if %errorlevel% neq 0 (
    echo [ERROR] Syntax check failed on problem.py
    pause
    exit /b 1
)

!PYTHON_CMD! -m py_compile mesh_pure.py >nul 2>&1
if %errorlevel% neq 0 (
    echo [ERROR] Syntax check failed on mesh_pure.py
    pause
    exit /b 1
)

echo       All syntax checks passed

rem ============================================================
rem Start server
rem ============================================================
echo.
echo [4/4] Starting server...
echo ============================================
echo   OK! Starting server on port 5000 ...
echo ============================================
echo.
echo   Press Ctrl+C to stop the server
echo.

!PYTHON_CMD! main.py
pause
