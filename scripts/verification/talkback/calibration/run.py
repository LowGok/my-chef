"""Separate Android/pulse audio calibration. No app code or customer data is installed."""
from contextlib import ExitStack
import json
import math
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time
import wave
import zipfile

NAME = "mychef_talkback"
SERIAL = "emulator-5554"
ROOT = Path(__file__).resolve().parent
SDK = Path(os.environ["ANDROID_HOME"])
ADB = str(SDK / "platform-tools/adb")
TOOLS = SDK / "build-tools/36.0.0"
JAR = SDK / "platforms/android-33/android.jar"
MODE = {"media": 440, "accessibility": 880, "speech": None}
EVENT = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
assert EVENT["repository"]["id"] == 1454481532 and EVENT["repository"]["private"] is False
assert EVENT["sender"]["login"] == "LowGok" and EVENT["ref"] == "refs/heads/ci/reminder-talkback-preflight-0f3e4a8"
assert os.environ["GITHUB_REPOSITORY"] == "LowGok/my-chef" and os.environ["GITHUB_ACTIONS"] == "true"


def call(*args, env=None, timeout=40):
    return subprocess.check_output(args, env=env, timeout=timeout, text=True).strip()


def stop(process, sig=signal.SIGTERM):
    if process.poll() is None:
        process.send_signal(sig)
        try:
            process.wait(timeout=6)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def until(condition, seconds, label):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        if condition():
            return
        time.sleep(.35)
    raise RuntimeError("Timed out checking " + label)


def sample(path, frequency):
    with wave.open(str(path), "rb") as audio:
        assert audio.getsampwidth() == 2 and audio.getframerate() == 16000 and audio.getnchannels() == 1
        data = memoryview(audio.readframes(audio.getnframes())).cast("h")
    if frequency is None:
        return max(map(abs, data), default=0) >= 64
    size = 800
    cos = [math.cos(2*math.pi*frequency*i/16_000) for i in range(size)]
    sin = [math.sin(2*math.pi*frequency*i/16_000) for i in range(size)]
    for start in range(0, len(data)-size, size):
        window = data[start:start+size]
        energy = sum(x*x for x in window)
        if energy < (64**2)*size:
            continue
        a, b = sum(x*y for x,y in zip(window,cos)), sum(x*y for x,y in zip(window,sin))
        if a*a+b*b > .25*size*energy:
            return True
    return False


def capture(state, env, action, frequency):
    path = state / "probe.wav"
    path.unlink(missing_ok=True)
    with (state/"capture.log").open("w") as log:
        process=subprocess.Popen(["ffmpeg","-nostdin","-v","error","-f","pulse","-i",NAME+".monitor",
                                  "-ac","1","-ar","16000","-c:a","pcm_s16le",str(path)],env=env,stdout=log,stderr=log)
        try:
            until(lambda: path.is_file() or process.poll() is not None,10,"pulse capture startup")
            if process.poll() is not None:
                raise RuntimeError("Pulse capture could not start")
            result=action()
            time.sleep(.6)
        finally:
            stop(process,signal.SIGINT)
    return result, sample(path,frequency)


def package(state, env):
    manifest = state / "AndroidManifest.xml"
    manifest.write_text("""<manifest xmlns:android="http://schemas.android.com/apk/res/android" package="org.mychef.speakprobe">
      <uses-sdk android:minSdkVersion="26" android:targetSdkVersion="33"/>
      <queries><intent><action android:name="android.intent.action.TTS_SERVICE"/></intent></queries>
      <application android:debuggable="true" android:label="Disposable audio probe"/>
      <instrumentation android:name=".SpeakerProbe" android:targetPackage="org.mychef.speakprobe"/>
    </manifest>""")
    classes, dex = state/"classes", state/"dex"
    classes.mkdir()
    dex.mkdir()
    object_jar, apk = state/"probe.jar", state/"probe.apk"
    call("javac","--release","17","-cp",str(JAR),"-d",str(classes),str(ROOT/"SpeakerProbe.java"),env=env)
    call("jar","--create","--file",str(object_jar),"-C",str(classes),".",env=env)
    call(str(TOOLS/"d8"),"--lib",str(JAR),"--min-api","26","--output",str(dex),str(object_jar),env=env)
    call(str(TOOLS/"aapt2"),"link","-I",str(JAR),"--manifest",str(manifest),"-o",str(apk),env=env)
    with zipfile.ZipFile(apk,"a") as archive:
        archive.write(dex/"classes.dex","classes.dex")
    key=state/"debug.keystore"
    call("keytool","-genkeypair","-keystore",str(key),"-storepass","android","-keypass","android","-keyalg","RSA",
         "-keysize","2048","-alias","androiddebugkey","-validity","2","-dname","CN=Disposable Device Probe",env=env)
    call(str(TOOLS/"apksigner"),"sign","--ks",str(key),"--ks-pass","pass:android","--key-pass","pass:android",str(apk),env=env)
    return apk


os.umask(0o077)
with tempfile.TemporaryDirectory(prefix="android-speaker-calibration-") as tmp, ExitStack() as stack:
    state=Path(tmp)
    for n in ("avd","pulse"):
        (state/n).mkdir(mode=0o700)
    socket=state/"pulse"/"native"
    env=dict(os.environ,ANDROID_AVD_HOME=str(state/"avd"),XDG_RUNTIME_DIR=str(state/"pulse"),
             PULSE_SERVER="unix:"+str(socket),PULSE_SINK=NAME,QEMU_AUDIO_DRV="pa")
    def adb(*args,timeout=45):
        return call(ADB,"-s",SERIAL,*args,env=env,timeout=timeout)
    pulse_log=stack.enter_context((state/"pulse.log").open("w"))
    pulse=subprocess.Popen(["pulseaudio","--daemonize=no","--use-pid-file=no","--exit-idle-time=-1","-n",
                            "-L","module-native-protocol-unix socket="+str(socket)+" auth-anonymous=1",
                            "-L","module-null-sink sink_name="+NAME+" rate=48000 channels=2"],
                           env=env,stdout=pulse_log,stderr=pulse_log)
    stack.callback(stop,pulse)
    until(socket.is_socket,12,"local pulse socket")
    assert NAME+".monitor" in call("pactl","list","short","sources",env=env)
    host=state/"host.wav"
    call("ffmpeg","-nostdin","-v","error","-f","lavfi","-i","sine=frequency=660:duration=1",str(host),env=env)
    _, host_pass=capture(state,env,lambda: call("paplay","--device="+NAME,str(host),env=env),660)
    if not host_pass:
        raise RuntimeError("Host capture failed its known tone; cannot classify Android playback")
    avd=SDK/"cmdline-tools/latest/bin/avdmanager"
    call(str(avd),"create","avd","--name","audio_calibration","--package","system-images;android-33;google_apis;x86_64",
         "--device","pixel_6","--path",str(state/"avd/audio_calibration.avd"),env=env)
    assert (state/"avd/audio_calibration.ini").is_file()
    boot_log=stack.enter_context((state/"emulator.log").open("w"))
    emulator=subprocess.Popen([str(SDK/"emulator/emulator"),"-avd","audio_calibration","-port","5554",
                              "-no-window","-no-snapshot","-no-boot-anim","-gpu","software"],
                             env=env,stdout=boot_log,stderr=boot_log)
    stack.callback(stop,emulator)
    def provisioned():
        if emulator.poll() is not None:
            raise RuntimeError("Stock calibration emulator exited before boot")
        try:
            return adb("shell","getprop","sys.boot_completed",timeout=3)=="1"
        except (subprocess.CalledProcessError,subprocess.TimeoutExpired):
            return False
    until(provisioned,240,"stock Android boot")
    apk=package(state,env)
    adb("install",str(apk))
    adb("shell","input","keyevent","KEYCODE_WAKEUP")
    adb("shell","wm","dismiss-keyguard")
    results={}
    for mode, frequency in MODE.items():
        def perform():
            raw=adb("shell","am","instrument","-w","-e","mode",mode,"org.mychef.speakprobe/.SpeakerProbe")
            status=re.search(r"INSTRUMENTATION_RESULT: probeStatus=([a-z-]{3,35})\b",raw)
            return status[1] if status else "instrumentation-unavailable"
        status, heard=capture(state,env,perform,frequency)
        results[mode]={"deviceStatus":status,"audioCaptured":heard}
    proof={"api":33,"hostToneCaptured":host_pass,"checks":results,"scope":"Playback calibration only, no TalkBack or MyChef proof."}
    Path(sys.argv[1]).write_text(json.dumps(proof,indent=2)+"\n")
    print(json.dumps(proof,indent=2))
