#!/bin/bash

# Centaur Connector Installer
# This script downloads and installs the connector binary with proper user directory structure

set -e

# Detect OS and architecture
detect_os() {
    case "$(uname -s)" in
        Darwin*)
            OS="darwin"
            ;;
        Linux*)
            OS="linux"
            ;;
        MINGW*|MSYS*|CYGWIN*)
            OS="windows"
            ;;
        *)
            echo "Unsupported OS: $(uname -s)"
            exit 1
            ;;
    esac
}

# Get latest release from GitHub API
get_latest_release() {
    # Use GitHub API to get latest release
    local release_info
    release_info=$(curl -sf "https://api.github.com/repos/shpak86/centaur/releases/latest")
    
    if [ $? -ne 0 ] || [ -z "$release_info" ]; then
        echo "v1.0.0"  # Fallback version
        return 1
    fi
    
    local version
    version=$(echo "$release_info" | grep '"tag_name"' | cut -d '"' -f 4)
    
    if [ -z "$version" ]; then
        echo "v1.0.0"  # Fallback version
        return 1
    else
        echo "$version"
        return 0
    fi
}

# Download binary with retry logic
download_with_retry() {
    local url=$1
    local output_file=$2
    local max_retries=3
    local retry_count=0
    
    while [ $retry_count -lt $max_retries ]; do
        if curl -Lf -o "$output_file" "$url"; then
            return 0
        else
            retry_count=$((retry_count + 1))
            echo "Download attempt $retry_count failed, retrying..."
            sleep 2
        fi
    done
    
    echo "Failed to download after $max_retries attempts"
    return 1
}

# Download binary
download_binary() {
    local binary_name=$1
    local os=$2
    local arch=$3
    local version=$4
    
    local url="https://github.com/shpak86/centaur/releases/download/${version}/${binary_name}-${os}-${arch}"
    
    echo "Downloading ${binary_name} for ${os}-${arch}..."
    if ! download_with_retry "$url" "${binary_name}"; then
        echo "Failed to download ${binary_name}"
        exit 1
    fi
    
    chmod +x "${binary_name}"
}

# Download config file
download_config() {
    local config_name=$1
    local version=$2
    
    local url="https://github.com/shpak86/centaur/releases/download/${version}/${config_name}"
    
    echo "Downloading ${config_name}..."
    if ! download_with_retry "$url" "$config_name"; then
        echo "Failed to download ${config_name}"
        exit 1
    fi
}

# Install connector binary and config
install_connector() {
    detect_os
    local version
    version=$(get_latest_release)
    
    echo "Installing Centaur Connector version $version for $OS"
    
    # Download binary
    download_binary "centaur-connector" "$OS" "amd64" "$version"
    
    # Download config file
    download_config "connector.json" "$version"
    
    # Create directories
    mkdir -p ~/.connector
    mkdir -p ~/.connector/config
    
    # Move binary to user directory
    mv centaur-connector ~/.connector/
    
    # Install config
    mv connector.json ~/.connector/config/connector.json
    
    echo "Connector installation complete!"
    echo "Configuration file located at: ~/.connector/config/connector.json"
    echo "Binary installed at: ~/.connector/centaur-connector"
}

# Main execution
main() {
    install_connector
}

main "$@"