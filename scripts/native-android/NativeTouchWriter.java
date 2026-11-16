package com.gkldevelopment.mychef.testing;

import android.system.Os;
import android.system.OsConstants;
import java.io.FileDescriptor;

/** Root-only app_process entry point in the disposable test APK, never MyChef. */
public final class NativeTouchWriter {
    public static void main(String[] args) {
        FileDescriptor fd = null; boolean failed = false; String reason = "arguments";
        System.err.println("ANALYTICS_TOUCH_STAGE=start");
        try {
            if (args.length != 8 || !args[1].matches("/dev/input/event[0-9]{1,3}")
                    || !args[7].matches("[01]")) throw new IllegalArgumentException();
            int x0=Integer.parseInt(args[2]),x1=Integer.parseInt(args[3]);
            int y0=Integer.parseInt(args[4]),y1=Integer.parseInt(args[5]),pressure=Integer.parseInt(args[6]);
            NativeTouchFrames.validate(args[0],x0,x1,y0,y1,pressure);
            reason = "permission";
            if (Os.getuid() != 0) throw new IllegalStateException();
            reason = "abi";
            if (!Os.uname().machine.equals("x86_64")) throw new IllegalStateException();
            reason = "open";
            fd = Os.open(args[1],OsConstants.O_WRONLY | OsConstants.O_CLOEXEC | OsConstants.O_NOFOLLOW,0);
            System.err.println("ANALYTICS_TOUCH_STAGE=open");
            if (!OsConstants.S_ISCHR(Os.fstat(fd).st_mode)) throw new IllegalStateException();
            System.err.println("ANALYTICS_TOUCH_STAGE=character");
            final FileDescriptor owned = fd;
            NativeTouchFrames.Sink sink = bytes -> {
                for (int offset=0;offset<bytes.length;) {
                    int written=Os.write(owned,bytes,offset,bytes.length-offset);
                    if (written<=0 || written%24!=0) throw new IllegalStateException();
                    offset+=written;
                }
            };
            reason = "write";
            try { NativeTouchFrames.write(args[0],x0,x1,y0,y1,pressure,args[7].equals("1"),sink); }
            catch (Exception failure) {
                // Release only, on the already verified descriptor. Never
                // retry an activation after a failed or interrupted write.
                if (!args[0].equals("release")) try {
                    NativeTouchFrames.write("release",x0,x1,y0,y1,pressure,args[7].equals("1"),sink);
                } catch (Exception ignored) { }
                throw failure;
            }
            System.err.println("ANALYTICS_TOUCH_STAGE=complete");
        } catch (Exception failure) {
            System.err.println("ANALYTICS_TOUCH_ERROR=" + reason);
            failed = true;
        } finally {
            if (fd != null) try { Os.close(fd); } catch (Exception ignored) { }
        }
        if (failed) System.exit(1);
    }
}
