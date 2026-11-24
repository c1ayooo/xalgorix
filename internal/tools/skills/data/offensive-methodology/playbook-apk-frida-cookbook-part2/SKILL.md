---
name: playbook-apk-frida-cookbook-part2
description: "Frida 实战脚本速查（第 2/2 部分）"
tags: [playbook, apk-reverse, mobile, frida, cookbook]
intent: offensive
---
## 脱壳 Hook

### 通用 DEX Dump

```javascript
Java.perform(function() {
    Java.enumerateClassLoaders({
        onMatch: function(loader) {
            try {
                var pathList = Java.cast(loader, Java.use("dalvik.system.BaseDexClassLoader")).pathList.value;
                var dexElements = pathList.dexElements.value;
                for (var i = 0; i < dexElements.length; i++) {
                    var dexFile = dexElements[i].dexFile.value;
                    if (dexFile) {
                        console.log("[DEX] " + dexFile.getName());
                        // 可以进一步 dump dex 内容
                    }
                }
            } catch(e) {}
        },
        onComplete: function() {}
    });
});
```

### Hook ClassLoader.loadClass

```javascript
Java.perform(function() {
    var ClassLoader = Java.use("java.lang.ClassLoader");
    ClassLoader.loadClass.overload('java.lang.String').implementation = function(name) {
        if (name.indexOf("com.target") !== -1) {
            console.log("[ClassLoader] " + name);
        }
        return this.loadClass(name);
    };
});
```

---

## 实用工具函数

```javascript
// 字节数组转十六进制
function bytesToHex(bytes) {
    if (!bytes) return "null";
    var hex = [];
    for (var i = 0; i < bytes.length; i++) {
        hex.push(('0' + (bytes[i] & 0xFF).toString(16)).slice(-2));
    }
    return hex.join('');
}

// 打印调用栈
function printStack() {
    console.log(Java.use("android.util.Log").getStackTraceString(
        Java.use("java.lang.Throwable").$new()));
}

// 打印对象所有字段
function printFields(obj) {
    var fields = obj.class.getDeclaredFields();
    fields.forEach(function(field) {
        field.setAccessible(true);
        try {
            console.log("  " + field.getName() + " = " + field.get(obj));
        } catch(e) {}
    });
}

// 搜索内存中的类实例
function findInstances(className) {
    Java.choose(className, {
        onMatch: function(instance) {
            console.log("[Instance] " + instance);
            printFields(instance);
        },
        onComplete: function() {}
    });
}
```

---

## 参考资源

| 资源 | 说明 | 链接 |
|------|------|------|
| Frida 官方文档 | API 参考 | https://frida.re/docs/ |
| Frida CodeShare | 社区脚本分享 | https://codeshare.frida.re/ |
| awesome-frida | 资源大全 | https://github.com/dweinstein/awesome-frida |
| frida-codeshare-scripts | 全网最全脚本收集 | https://github.com/zengfr/frida-codeshare-scripts |
| Objection | Frida 封装工具 | https://github.com/sensepost/objection |
| r2frida | radare2 + Frida 集成 | https://github.com/nowsecure/r2frida |

> （续前：playbook-apk-frida-cookbook 第 2/2 部分）
