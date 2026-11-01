var PHP_PREFIX="C:\\php"
var PHP_ZTS="No"
var VC_VERSION=1944
var PHP_VERSION=8
var PHP_MINOR_VERSION=4
var FSO = WScript.CreateObject("Scripting.FileSystemObject");
re = /\\script/i;
var PHP_DIR=FSO.GetParentFolderName(WScript.ScriptFullName).replace(re,"");

var modules = "";
C.WriteLine("var PHP_PREFIX = " + '"' + PHP_PREFIX.replace(new RegExp('(["\\\\])', "g"), '\\$1') + '"');
