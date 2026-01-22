# 安装完整的编译工具链
```bash
sudo apt install -y build-essential clang gcc python3-dev patchelf upx
```

# 使用nuitka 编译
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
  main.py


