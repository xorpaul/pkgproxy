#! /usr/bin/env bash
set -e

if [ $# -ne 2 ]; then
  echo "need the version number and release comment as argument"
  echo "e.g. ${0} 0.4.5 'fix local modules and modules with install_path purging bug #80 #82'"
  echo "Aborting..."
  exit 1
fi
#
time go test -v
#
# Remove leading 'v' from the version number if present
version=${1#v}
#
if [ $? -ne 0 ]; then
  echo "Tests unsuccessful"
  echo "Aborting..."
  exit 1
fi
#
#
echo "creating git tag v${version}"
git tag v${version}
echo "pushing git tag v${version}"
git push -f --tags
git push

# Clean and create build directory
echo "cleaning and creating build directory"
rm -rf build
mkdir -p build

# try to get the project name from the current working directory
projectname=${PWD##*/}
upx=$(which upx)
export CGO_ENABLED=0
export BUILDTIME=$(date -u '+%Y-%m-%d_%H:%M:%S')
export BUILDVERSION=$(git describe --tags)

build() {
  echo "building ${projectname}-$1-$2 with version ${version}"
  env GOOS=$1 GOARCH=$2 go build -ldflags "-X main.buildtime=${BUILDTIME} -X main.buildversion=${BUILDVERSION}" -o build/${projectname}-v${version}-$1-$2
  if [ ${#upx} -gt 0 ]; then
    if [ $1 == "linux" ]; then
      $upx build/${projectname}-v${version}-$1-$2
    fi
  fi
}

for os in darwin linux; do
  for arch in arm64 amd64; do
    build $os $arch
  done
done

gh auth status >/dev/null 2>&1 && echo "creating github release v${version}"
gh auth status >/dev/null 2>&1 && gh release create --fail-on-no-commits --verify-tag --repo xorpaul/${projectname} --title "v${version}" --notes "${2}" v${version} ./build/${projectname}-v${version}* || echo "skipping github-release as gh auth status failed"
