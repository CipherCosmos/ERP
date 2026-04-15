#!/bin/bash

# GBOS ERP - Database Reset Script
# Safely removes and recreates all tables with fresh schema
# WARNING: This will DELETE all data. Use only for testing/development!

set -e

DB_PATH="${DATABASE_PATH:-./data/gbos.db}"

echo "╔════════════════════════════════════════════════════════════════╗"
echo "║         GBOS ERP - DATABASE RESET UTILITY                     ║"
echo "╚════════════════════════════════════════════════════════════════╝"
echo ""
echo "⚠️  WARNING: This script will PERMANENTLY DELETE all data!"
echo "Database: $DB_PATH"
echo ""
read -p "Are you absolutely sure? Type 'yes' to continue: " confirm

if [ "$confirm" != "yes" ]; then
    echo "❌ Cancelled. No changes made."
    exit 0
fi

# Backup existing database
if [ -f "$DB_PATH" ]; then
    backup_file="${DB_PATH}.backup.$(date +%s)"
    echo "📦 Creating backup: $backup_file"
    cp "$DB_PATH" "$backup_file"
    echo "✓ Backup created"

    echo "🗑️  Removing old database..."
    rm "$DB_PATH"
fi

# Create schema migrations directory if needed
mkdir -p ./migrations

echo "🔄 Database will be recreated on next application startup"
echo "✓ Ready for fresh test data"
echo ""
echo "Next steps:"
echo "1. Start the application: go run cmd/server/main.go"
echo "2. Run the E2E tests: bash scripts/comprehensive-e2e-test.sh"
