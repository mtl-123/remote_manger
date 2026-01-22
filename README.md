nuitka \

  --onefile \

  --follow-imports \

  --nofollow-import-to=tkinter,matplotlib \

  --enable-plugin=upx \

  --lto=yes \

  --assume-yes-for-downloads \

  --output-filename=remote-manager \

  --output-dir=dist \

  --include-package=psutil \

  --include-package=yaml \

  --remove-output \

  new.py



# 安装完整的编译工具链

 sudo apt install -y build-essential clang gcc python3-dev patchelf upx
