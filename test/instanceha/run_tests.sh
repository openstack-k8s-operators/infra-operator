#!/bin/bash

# InstanceHA Comprehensive Test Runner
# This script runs both unit tests and functional tests for the InstanceHA module
# Includes comprehensive coverage of kdump functionality and all other features

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}   InstanceHA Comprehensive Tests      ${NC}"
echo -e "${BLUE}   (Unit + Functional + Integration)   ${NC}"
echo -e "${BLUE}========================================${NC}"
echo

# Get the directory of this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Create and activate virtual environment for dependencies
VENV_DIR="${SCRIPT_DIR}/.venv"
if [ -f "${SCRIPT_DIR}/requirements.txt" ]; then
    echo -e "${BLUE}Setting up Python virtual environment...${NC}"

    # Create virtual environment if it doesn't exist
    if [ ! -d "${VENV_DIR}" ]; then
        python3 -m venv "${VENV_DIR}"
    fi

    # Activate virtual environment
    source "${VENV_DIR}/bin/activate"

    # Install dependencies
    echo -e "${BLUE}Installing Python dependencies...${NC}"
    pip install -q -r "${SCRIPT_DIR}/requirements.txt"
    echo -e "${GREEN}Dependencies installed${NC}"
    echo
fi

# Set PYTHONPATH to include instanceha.py location
INSTANCEHA_DIR="$(cd "${SCRIPT_DIR}/../../templates/instanceha/bin" && pwd)"
export PYTHONPATH="${INSTANCEHA_DIR}:${SCRIPT_DIR}:${PYTHONPATH}"

# Function to run tests and capture results
run_test_file() {
    local test_file="$1"
    local test_name="$2"

    echo -e "${YELLOW}Running ${test_name}...${NC}"
    echo "----------------------------------------"

    # Run tests with warning suppression for missing config files
    if python3 "${SCRIPT_DIR}/${test_file}" 2>&1 | grep -v "WARNING:root:.*file not found" | grep -v "WARNING:root:.*using defaults"; then
        EXIT_CODE=${PIPESTATUS[0]}
        if [ $EXIT_CODE -eq 0 ]; then
            echo -e "${GREEN}[PASS] ${test_name} completed successfully${NC}"
            return 0
        else
            echo -e "${RED}[FAIL] ${test_name} failed${NC}"
            return 1
        fi
    else
        echo -e "${RED}[FAIL] ${test_name} failed${NC}"
        return 1
    fi
}

# Initialize test results
unit_core_tests_passed=0
fencing_tests_passed=0
kdump_tests_passed=0
security_tests_passed=0
critical_error_tests_passed=0
evacuation_workflow_tests_passed=0
config_features_tests_passed=0
helper_functions_tests_passed=0
functional_tests_passed=0
integration_tests_passed=0
region_isolation_tests_passed=0

# Run core unit tests
echo -e "${BLUE}Running Core Unit Tests...${NC}"
if run_test_file "test_unit_core.py" "Core Unit Tests"; then
    unit_core_tests_passed=1
fi

echo
echo

# Run fencing agents tests
echo -e "${BLUE}Running Fencing Agents Tests...${NC}"
if run_test_file "test_fencing_agents.py" "Fencing Agents Tests"; then
    fencing_tests_passed=1
fi

echo
echo

# Run kdump detection tests
echo -e "${BLUE}Running Kdump Detection Tests...${NC}"
if run_test_file "test_kdump_detection.py" "Kdump Detection Tests"; then
    kdump_tests_passed=1
fi

echo
echo

# Run security validation tests
echo -e "${BLUE}Running Security Validation Tests...${NC}"
if run_test_file "test_security_validation.py" "Security Validation Tests"; then
    security_tests_passed=1
fi

echo
echo

# Run critical error path tests
echo -e "${BLUE}Running Critical Error Path Tests...${NC}"
if run_test_file "test_critical_error_paths.py" "Critical Error Path Tests"; then
    critical_error_tests_passed=1
fi

echo
echo

# Run evacuation workflow tests
echo -e "${BLUE}Running Evacuation Workflow Tests...${NC}"
if run_test_file "test_evacuation_workflow.py" "Evacuation Workflow Tests"; then
    evacuation_workflow_tests_passed=1
fi

echo
echo

# Run configuration feature tests
echo -e "${BLUE}Running Configuration Feature Tests...${NC}"
if run_test_file "test_config_features.py" "Configuration Feature Tests"; then
    config_features_tests_passed=1
fi

echo
echo

# Run helper functions tests
echo -e "${BLUE}Running Helper Functions Tests...${NC}"
if run_test_file "test_helper_functions.py" "Helper Functions Tests"; then
    helper_functions_tests_passed=1
fi

echo
echo

# Run functional tests
echo -e "${BLUE}Running Functional Tests...${NC}"
if run_test_file "functional_test.py" "Functional Tests"; then
    functional_tests_passed=1
fi

echo
echo

# Run integration tests
echo -e "${BLUE}Running Integration Tests...${NC}"
if run_test_file "integration_test.py" "Integration Tests"; then
    integration_tests_passed=1
fi

echo
echo

# Run region isolation tests
echo -e "${BLUE}Running Region Isolation Tests...${NC}"
if run_test_file "test_region_isolation.py" "Region Isolation Tests"; then
    region_isolation_tests_passed=1
fi

echo
echo

# Summary
echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}            Test Summary                ${NC}"
echo -e "${BLUE}========================================${NC}"

total_passed=$((unit_core_tests_passed + fencing_tests_passed + kdump_tests_passed + security_tests_passed + critical_error_tests_passed + evacuation_workflow_tests_passed + config_features_tests_passed + helper_functions_tests_passed + functional_tests_passed + integration_tests_passed + region_isolation_tests_passed))
total_tests=11

if [ $unit_core_tests_passed -eq 1 ]; then
    echo -e "${GREEN}[PASS] Core Unit Tests: PASSED${NC}"
else
    echo -e "${RED}[FAIL] Core Unit Tests: FAILED${NC}"
fi

if [ $fencing_tests_passed -eq 1 ]; then
    echo -e "${GREEN}[PASS] Fencing Agents Tests: PASSED${NC}"
else
    echo -e "${RED}[FAIL] Fencing Agents Tests: FAILED${NC}"
fi

if [ $kdump_tests_passed -eq 1 ]; then
    echo -e "${GREEN}[PASS] Kdump Detection Tests: PASSED${NC}"
else
    echo -e "${RED}[FAIL] Kdump Detection Tests: FAILED${NC}"
fi

if [ $security_tests_passed -eq 1 ]; then
    echo -e "${GREEN}[PASS] Security Validation Tests: PASSED${NC}"
else
    echo -e "${RED}[FAIL] Security Validation Tests: FAILED${NC}"
fi

if [ $critical_error_tests_passed -eq 1 ]; then
    echo -e "${GREEN}[PASS] Critical Error Path Tests: PASSED${NC}"
else
    echo -e "${RED}[FAIL] Critical Error Path Tests: FAILED${NC}"
fi

if [ $evacuation_workflow_tests_passed -eq 1 ]; then
    echo -e "${GREEN}[PASS] Evacuation Workflow Tests: PASSED${NC}"
else
    echo -e "${RED}[FAIL] Evacuation Workflow Tests: FAILED${NC}"
fi

if [ $config_features_tests_passed -eq 1 ]; then
    echo -e "${GREEN}[PASS] Configuration Feature Tests: PASSED${NC}"
else
    echo -e "${RED}[FAIL] Configuration Feature Tests: FAILED${NC}"
fi

if [ $helper_functions_tests_passed -eq 1 ]; then
    echo -e "${GREEN}[PASS] Helper Functions Tests: PASSED${NC}"
else
    echo -e "${RED}[FAIL] Helper Functions Tests: FAILED${NC}"
fi

if [ $functional_tests_passed -eq 1 ]; then
    echo -e "${GREEN}[PASS] Functional Tests: PASSED${NC}"
else
    echo -e "${RED}[FAIL] Functional Tests: FAILED${NC}"
fi

if [ $integration_tests_passed -eq 1 ]; then
    echo -e "${GREEN}[PASS] Integration Tests: PASSED${NC}"
else
    echo -e "${RED}[FAIL] Integration Tests: FAILED${NC}"
fi

if [ $region_isolation_tests_passed -eq 1 ]; then
    echo -e "${GREEN}[PASS] Region Isolation Tests: PASSED${NC}"
else
    echo -e "${RED}[FAIL] Region Isolation Tests: FAILED${NC}"
fi

echo
echo -e "${BLUE}Total: ${total_passed}/${total_tests} test suites passed${NC}"

if [ $total_passed -eq $total_tests ]; then
    echo -e "${GREEN}[PASS] All tests completed successfully!${NC}"
    exit 0
else
    echo -e "${RED}WARNING: Some tests failed. Please review the output above.${NC}"
    exit 1
fi
