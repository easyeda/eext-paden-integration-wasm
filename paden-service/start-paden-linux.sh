#!/usr/bin/env bash
set -e

echo "============================================"
echo "  paden-service build and start"
echo "============================================"
echo

# ============================================
# Step 0: Environment Check
# ============================================
echo "[0/4] Checking environment..."

# Find Python
PYTHON=""
if command -v python3 &>/dev/null; then
    PYTHON=python3
elif command -v python &>/dev/null; then
    PYTHON=python
else
    echo "[ERROR] Python not found"
    echo
    echo "Please install Python 3.10+ from https://www.python.org/"
    exit 1
fi

# Get Python version
PY_VER=$($PYTHON --version 2>&1 | awk '{print $2}')
echo "       Found: $PY_VER"

# Check if version is at least 3.10
PY_MAJOR=$(echo $PY_VER | cut -d. -f1)
PY_MINOR=$(echo $PY_VER | cut -d. -f2)

if [ "$PY_MAJOR" -lt 3 ] || ([ "$PY_MAJOR" -eq 3 ] && [ "$PY_MINOR" -lt 10 ]); then
    echo "[ERROR] Python version too old: $PY_VER"
    echo
    echo "This project requires Python 3.10 or higher."
    echo "The code uses 'match/case' syntax (Python 3.10+)."
    echo
    echo "Current version: $PY_VER"
    echo "Required version: 3.10 or higher"
    echo
    echo "Please download and install Python 3.10+:"
    echo "  https://www.python.org/downloads/"
    echo
    echo "After installation, restart this script."
    exit 1
fi

echo "       Python $PY_VER - OK"
echo "       Environment check passed"
echo

# Change to script directory
cd "$(dirname "$0")"

# ============================================
# Step 1: Check local files
# ============================================
echo "[1/4] Checking local files..."

if [ ! -f "main.py" ]; then
    echo "[ERROR] main.py not found"
    echo
    echo "Please make sure this script is placed inside the paden-service directory:"
    echo "  paden-service/start-paden-linux.sh    <- here"
    echo "  paden-service/main.py"
    echo "  paden-service/problem.py"
    echo "  paden-service/solver_enhanced.py"
    echo "  paden-service/mesh_pure.py"
    echo "  ..."
    exit 1
fi

if [ ! -f "problem.py" ]; then
    echo "[ERROR] problem.py not found"
    exit 1
fi

if [ ! -f "solver.py" ] && [ ! -f "solver_enhanced.py" ]; then
    echo "[ERROR] Neither solver.py nor solver_enhanced.py found"
    exit 1
fi

echo "       All required files present"
echo

# ============================================
# Step 2: Check and install dependencies
# ============================================
echo "[2/4] Checking dependencies..."
MISSING_DEPS=0

# Standard packages
for pkg in numpy scipy shapely fastapi uvicorn pydantic psutil matplotlib trimesh; do
    if ! $PYTHON -c "import $pkg" 2>/dev/null; then
        MISSING_DEPS=1
        break
    fi
done

# Check pygerber separately (version-specific import)
if ! $PYTHON -c "from pygerber.gerberx3.api.v2 import GerberFile" 2>/dev/null && \
   ! $PYTHON -c "import pygerber.gerber.api" 2>/dev/null; then
    MISSING_DEPS=1
fi

if [ $MISSING_DEPS -eq 0 ]; then
    echo "       All dependencies installed"
else
    echo "       Installing missing packages..."

    # Try official PyPI first
    if $PYTHON -m pip install numpy scipy shapely fastapi uvicorn pydantic psutil matplotlib trimesh "pygerber>=3.0.0a3" --pre --quiet --upgrade 2>/dev/null; then
        echo "       Installation successful!"
    else
        # Try Tsinghua mirror (China)
        echo "       [WARN] Official PyPI failed, trying Tsinghua mirror..."
        if $PYTHON -m pip install numpy scipy shapely fastapi uvicorn pydantic psutil matplotlib trimesh "pygerber>=3.0.0a3" --pre --quiet --upgrade -i https://pypi.tuna.tsinghua.edu.cn/simple 2>/dev/null; then
            echo "       Installation successful!"
        else
            # Try Aliyun mirror (China)
            echo "       [WARN] Trying Aliyun mirror..."
            if $PYTHON -m pip install numpy scipy shapely fastapi uvicorn pydantic psutil matplotlib trimesh "pygerber>=3.0.0a3" --pre --quiet --upgrade -i https://mirrors.aliyun.com/pypi/simple/ 2>/dev/null; then
                echo "       Installation successful!"
            else
                echo "[ERROR] All installation attempts failed"
                echo
                echo "Please try manually:"
                echo "  pip install numpy scipy shapely fastapi uvicorn pydantic matplotlib trimesh psutil"
                echo "  pip install 'pygerber>=3.0.0a3' --pre"
                echo
                echo "Or use a mirror:"
                echo "  pip install -i https://pypi.tuna.tsinghua.edu.cn/simple 'pygerber>=3.0.0a3' --pre"
                exit 1
            fi
        fi
    fi
fi
echo

# ============================================
# Step 3: Syntax check
# ============================================
echo "[3/4] Syntax check..."

if ! $PYTHON -c "import main" 2>&1; then
    echo "[ERROR] Syntax check failed on main.py"
    exit 1
fi

if [ -f "solver.py" ]; then
    if ! $PYTHON -m py_compile solver.py 2>/dev/null; then
        echo "[ERROR] Syntax check failed on solver.py"
        exit 1
    fi
fi

if [ -f "solver_enhanced.py" ]; then
    if ! $PYTHON -c "import solver_enhanced" 2>&1; then
        echo "[ERROR] Syntax check failed on solver_enhanced.py"
        exit 1
    fi
fi

if ! $PYTHON -m py_compile problem.py 2>/dev/null; then
    echo "[ERROR] Syntax check failed on problem.py"
    exit 1
fi

if ! $PYTHON -m py_compile mesh_pure.py 2>/dev/null; then
    echo "[ERROR] Syntax check failed on mesh_pure.py"
    exit 1
fi

echo "       All syntax checks passed"
echo

# ============================================
# Step 4: Start server
# ============================================
echo "[4/4] Starting server..."
echo "============================================"
echo "  OK! Starting server on port 5000 ..."
echo "============================================"
echo "  Press Ctrl+C to stop the server"
echo

$PYTHON main.py
