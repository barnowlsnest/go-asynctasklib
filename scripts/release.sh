#!/bin/bash

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to print colored output
print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check if we're in a git repository
if ! git rev-parse --git-dir > /dev/null 2>&1; then
    print_error "Not in a git repository"
    exit 1
fi

# Check if working directory is clean
if ! git diff-index --quiet HEAD --; then
    print_error "Working directory is not clean. Please commit or stash your changes."
    exit 1
fi

# Check if we're on main branch
current_branch=$(git rev-parse --abbrev-ref HEAD)
if [ "$current_branch" != "main" ]; then
    print_warning "You are not on the main branch (current: $current_branch)"
    read -p "Do you want to continue? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        print_info "Release cancelled"
        exit 0
    fi
fi

# Get the current version from git tags
current_version=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
print_info "Current version: $current_version"

# Remove 'v' prefix for version calculation
version_number=${current_version#v}

# Split version into parts
IFS='.' read -ra VERSION_PARTS <<< "$version_number"
major=${VERSION_PARTS[0]:-0}
minor=${VERSION_PARTS[1]:-0}
patch=${VERSION_PARTS[2]:-0}

# Determine version bump type
bump_type=${1:-patch}

case $bump_type in
    major)
        major=$((major + 1))
        minor=0
        patch=0
        ;;
    minor)
        minor=$((minor + 1))
        patch=0
        ;;
    patch)
        patch=$((patch + 1))
        ;;
    *)
        print_error "Invalid bump type: $bump_type. Use 'major', 'minor', or 'patch'"
        exit 1
        ;;
esac

new_version="v$major.$minor.$patch"
print_info "New version will be: $new_version"

# Confirm release
read -p "Do you want to create release $new_version? (y/N): " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    print_info "Release cancelled"
    exit 0
fi

# Check if unexpected changes are present
if ! git diff-index --quiet HEAD --; then
    print_warning "There are uncommitted changes. Please commit them first."
    git diff --name-only
    exit 1
fi

print_success "Ready for release $new_version"

# Create annotated tag
print_info "Creating annotated tag $new_version..."
git tag -a "$new_version" -m "Release $new_version

Changes since $current_version:
$(git log --oneline "$current_version"..HEAD --pretty=format:"- %s" | head -20)
"

print_success "Tag $new_version created successfully"

# Ask if user wants to push
read -p "Do you want to push the tag to origin? (y/N): " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    print_info "Pushing tag to origin..."
    git push origin "$new_version"
    print_success "Tag pushed to origin"

    print_info "Tag is now available for Go module users:"
    print_info "go get ${MODULE_PATH}@$new_version"
else
    print_warning "Tag created locally but not pushed to origin"
    print_info "To push later, run: git push origin $new_version"
fi

print_success "Release $new_version completed successfully!"
